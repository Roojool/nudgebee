package observability

import (
	"strings"
	"testing"
	"time"

	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Golden strings, per the builders' convention: a template change is caught here
// rather than re-derived.
func TestBuildDatadogHTTPQuery(t *testing.T) {
	meta := RequestMetadata{Kind: "deployment", Namespace: "nudgebee", Name: "services-server",
		APMOperation: "http.server.request", RangeWindow: "3600s"}

	cases := []struct{ key, expected string }{
		{"http_throughput", "sum:trace.http.server.request.hits{service:services-server} by {resource_name}.as_count().rollup(sum, 3600)"},
		{"http_latency_p95", "p95:trace.http.server.request{service:services-server} by {resource_name}.rollup(3600)"},
		{"http_latency_p99", "p99:trace.http.server.request{service:services-server} by {resource_name}.rollup(3600)"},
		{"http_latency_sum", "sum:trace.http.server.request{service:services-server} by {resource_name}.rollup(sum, 3600)"},
		{"http_error_rate", "(sum:trace.http.server.request.errors{service:services-server} by {resource_name}.as_count().rollup(sum, 3600) / sum:trace.http.server.request.hits{service:services-server} by {resource_name}.as_count().rollup(sum, 3600)) * 100"},
		{"http_status", "sum:trace.http.server.request.hits.by_http_status{service:services-server} by {http.status_code}.as_rate()"},
		{"http_max_response_time", "max:trace.http.server.request{service:services-server}"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			assert.Equal(t, tc.expected, buildDatadogHTTPQuery(tc.key, meta))
		})
	}
}

// Through the workload builder: the HTTP keys follow the probed operation, and are
// left out entirely when no operation was found. They used to query
// trace.servlet.request, which only Datadog's Java tracer emits.
func TestBuildDatadogWorkloadQueries_HTTPKeysFollowTheProbedOperation(t *testing.T) {
	meta := RequestMetadata{Kind: "deployment", Namespace: "shop", Name: "checkout",
		APMOperation: "servlet.request", RangeWindow: "7200s"}
	queries := buildDatadogWorkloadQueries(meta, []string{"http_latency_p99", "cpu_request"})
	assert.Equal(t, "p99:trace.servlet.request{service:checkout} by {resource_name}.rollup(7200)", queries["http_latency_p99"])
	assert.Contains(t, queries["cpu_request"], "kube_deployment:checkout", "non-HTTP keys keep their kube_* filter")

	meta.APMOperation = ""
	queries = buildDatadogWorkloadQueries(meta, []string{"http_throughput", "http_status", "cpu_request"})
	assert.NotContains(t, queries, "http_throughput", "no operation found: no query, so no misleading empty series")
	assert.NotContains(t, queries, "http_status")
	assert.Contains(t, queries, "cpu_request")
}

func TestPickDatadogAPMOperation(t *testing.T) {
	series := func(name string, values ...float64) Result {
		return Result{Metric: map[string]string{"__name__": name}, Values: values}
	}

	// Instrumented twice: the busier operation is the one serving the traffic.
	out := OutputMetricQuery{Results: []QueryResult{{QueryKey: "apm_operation_probe", Payload: []Result{
		series("trace.servlet.request.hits", 5),
		series("trace.http.server.request.hits", 9467),
	}}}}
	assert.Equal(t, "http.server.request", pickDatadogAPMOperation(out))

	assert.Equal(t, "", pickDatadogAPMOperation(OutputMetricQuery{}), "nothing reported")
	zero := OutputMetricQuery{Results: []QueryResult{{Payload: []Result{series("trace.http.request.hits", 0)}}}}
	assert.Equal(t, "", pickDatadogAPMOperation(zero), "present but no hits in the window")
	unknown := OutputMetricQuery{Results: []QueryResult{{Payload: []Result{series("trace.some.other.hits", 50)}}}}
	assert.Equal(t, "", pickDatadogAPMOperation(unknown), "only listed candidates are chosen")
}

func TestSplitDatadogResource(t *testing.T) {
	cases := []struct{ resource, method, path string }{
		{"post_/rpc/metrics", "POST", "/rpc/metrics"},
		// Only the first "_/" separates method from path; later underscores are the path's.
		{"post_/v1/cloud/get_metrics", "POST", "/v1/cloud/get_metrics"},
		{"get_/api/products/_productid_/index", "GET", "/api/products/_productid_/index"},
		{"get_/", "GET", "/"},
		{"get", "GET", ""},
		{"/healthz", "", "/healthz"},
	}
	for _, tc := range cases {
		method, path := splitDatadogResource(tc.resource)
		assert.Equalf(t, tc.method, method, "method of %q", tc.resource)
		assert.Equalf(t, tc.path, path, "path of %q", tc.resource)
	}
}

// The latency table keys its rows on method + path and the status chart reads
// "status" — the labels the Prometheus builder emits. A user's own query is untouched.
func TestDatadogHTTPLabels(t *testing.T) {
	results := []QueryResult{
		{QueryKey: "http_throughput", Payload: []Result{{Metric: map[string]string{"resource_name": "post_/rpc/metrics"}}}},
		{QueryKey: "http_status", Payload: []Result{{Metric: map[string]string{"http.status_code": "404"}}}},
		{QueryKey: "user_query", Payload: []Result{{Metric: map[string]string{"resource_name": "post_/x"}}}},
		{QueryKey: "http_max_response_time", Payload: []Result{{}}},
	}

	datadogHTTPLabels(results)

	assert.Equal(t, "POST", results[0].Payload[0].Metric["method"])
	assert.Equal(t, "/rpc/metrics", results[0].Payload[0].Metric["path"])
	assert.Equal(t, "404", results[1].Payload[0].Metric["status"])
	assert.NotContains(t, results[2].Payload[0].Metric, "method", "not an HTTP key")
}

// The workload name reaches the query from the request. A value that could close the
// metric scope or add a clause is refused, so nothing is queried at all.
func TestDatadogHTTPQueryRefusesUnsafeWorkloadNames(t *testing.T) {
	for _, name := range []string{"services-server", "my_svc.v2", "Checkout"} {
		assert.Truef(t, datadogMetricTagValueSafe(name), "%q is a legitimate name", name)
	}
	for _, name := range []string{"", "x}", "a,b", "a b", "svc:*", "x{y", `a"b`} {
		assert.Falsef(t, datadogMetricTagValueSafe(name), "%q must be refused", name)
		meta := RequestMetadata{Name: name, APMOperation: "http.server.request", RangeWindow: "3600s"}
		assert.Emptyf(t, buildDatadogHTTPQuery("http_throughput", meta), "no query for %q", name)
	}
	// The probe refuses before making any call: it would panic on the nil context otherwise.
	assert.Equal(t, "", probeDatadogAPMOperation(nil, GetUtilisationTrendRequest{}, "datadog", "user", RequestMetadata{Name: "x},sum:other{*"}))
}

func TestDatadogRollupSeconds(t *testing.T) {
	assert.Equal(t, 86400, datadogRollupSeconds("86400s"))
	assert.Equal(t, 3600, datadogRollupSeconds(""), "unit-test metadata: the Prometheus keys' one hour")
	assert.Equal(t, 3600, datadogRollupSeconds("junk"))
}

// ddHTTPFakeSource answers the operation probe and the HTTP queries the way the real
// Datadog source would, and records what it was asked.
type ddHTTPFakeSource struct {
	MetricSource
	asked []FetchMetricsRequest
}

func (f *ddHTTPFakeSource) FetchMetricsQuery(_ *security.RequestContext, req FetchMetricsRequest) (OutputMetricQuery, error) {
	f.asked = append(f.asked, req)
	out := OutputMetricQuery{}
	for key, query := range req.Queries {
		qr := QueryResult{QueryKey: key, Query: query}
		switch {
		case key == "apm_operation_probe":
			qr.Payload = []Result{{Metric: map[string]string{"__name__": "trace.http.server.request.hits"}, Values: []float64{9467}}}
		case strings.Contains(query, "trace.http.server.request"):
			qr.Payload = []Result{{
				Metric:     map[string]string{"__name__": "trace.http.server.request.hits", "resource_name": "post_/rpc/metrics"},
				Timestamps: []int64{1700000000},
				Values:     []float64{369},
			}}
		}
		out.Results = append(out.Results, qr)
	}
	return out, nil
}

// End to end through FetchMetricUtilisation: the request the latency table sends on a
// Datadog account finds the operation, asks for it by service, and comes back with the
// method/path rows the table reads.
func TestFetchMetricUtilisation_DatadogHTTPTable(t *testing.T) {
	origProvider, origSource := utilisationProviderForAccount, metricsSourceForAccount
	t.Cleanup(func() { utilisationProviderForAccount, metricsSourceForAccount = origProvider, origSource })
	utilisationProviderForAccount = func(*security.RequestContext, string, string, string, string) (string, string, error) {
		return "datadog", "user", nil
	}
	source := &ddHTTPFakeSource{}
	metricsSourceForAccount = func(*security.RequestContext, string, string, string) (MetricSource, error) {
		return source, nil
	}

	end := time.Now()
	out, err := FetchMetricUtilisation(testRequestContext(), GetUtilisationTrendRequest{
		AccountId: "acc",
		StartTime: end.Add(-time.Hour).UnixMilli(),
		EndTime:   end.UnixMilli(),
		Request: map[string]any{
			"kind":               "workload",
			"workload_namespace": "nudgebee",
			"workload_name":      "services-server",
			"metrics":            []any{"http_throughput"},
			"instant":            true,
		},
	})
	require.NoError(t, err)

	require.Len(t, source.asked, 2, "one probe, then the table's query")
	assert.Contains(t, source.asked[0].Queries["apm_operation_probe"], "sum:trace.servlet.request.hits{service:services-server}")
	assert.Equal(t, "sum:trace.http.server.request.hits{service:services-server} by {resource_name}.as_count().rollup(sum, 3600)",
		source.asked[1].Queries["http_throughput"])

	require.Len(t, out.Results, 1)
	row := out.Results[0].Payload[0]
	assert.Equal(t, "POST", row.Metric["method"])
	assert.Equal(t, "/rpc/metrics", row.Metric["path"])
	assert.Equal(t, []float64{369}, row.Values)
}
