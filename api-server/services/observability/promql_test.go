package observability

import (
	"errors"
	"net/http"
	"nudgebee/services/eventrule/playbooks"
	"nudgebee/services/integrations"
	"nudgebee/services/security"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUserPrometheus routes every FetchMetricsQuery in the test at a fake
// user-connected Prometheus: the source resolver is bypassed (no integration
// tables) and the connection config points at an httptest server whose
// requests are recorded.
func stubUserPrometheus(t *testing.T, additionalLabels string, handler http.HandlerFunc) *requestRecorder {
	t.Helper()
	recorder := newPrometheusSaasServer(t, handler)
	origLoader := loadPrometheusUserConfig
	cfgTemplate, _ := origLoader(testRequestContext(), "any")
	loadPrometheusUserConfig = func(*security.RequestContext, string) (integrations.PrometheusUserConfig, error) {
		cfg := cfgTemplate
		cfg.AdditionalLabels = additionalLabels
		return cfg, nil
	}
	origResolve := metricsSourceForAccount
	metricsSourceForAccount = func(_ *security.RequestContext, _ string, provider string, _ string) (MetricSource, error) {
		assert.Equal(t, "prometheus", provider, "internal PromQL callers name the provider explicitly")
		return &PrometheusSaasMetricSource{}, nil
	}
	t.Cleanup(func() { loadPrometheusUserConfig = origLoader; metricsSourceForAccount = origResolve })
	return recorder
}

func promMatrixResponse(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"pod":"api-1","namespace":"shop"},"values":[[1700000000,"1"],[1700000060,"2.5"]]}]}}`))
}

func promVectorResponse(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"pod":"api-1","pod_ip":"10.0.0.1","created_by_kind":"ReplicaSet","created_by_name":"api-abc"},"value":[1700000000,"1"]}]}}`))
}

func TestPromQLLabels_ReadsTheLabelSetsForOneQueryKey(t *testing.T) {
	out := OutputMetricQuery{Results: []QueryResult{
		{QueryKey: "pod_info", Payload: []Result{
			{Metric: map[string]string{"pod": "api-1", "pod_ip": "10.0.0.1"}},
			{Metric: map[string]string{"pod": "api-2", "pod_ip": "10.0.0.2"}},
		}},
		{QueryKey: "rs_owner", Payload: []Result{{Metric: map[string]string{"replicaset": "api-abc"}}}},
	}}
	labels := PromQLLabels(out, "pod_info")
	require.Len(t, labels, 2)
	assert.Equal(t, "10.0.0.2", labels[1]["pod_ip"])
	assert.Equal(t, "api-abc", PromQLLabels(out, "rs_owner")[0]["replicaset"])
	assert.Nil(t, PromQLLabels(out, "missing"))
	assert.Nil(t, PromQLLabels(OutputMetricQuery{}, "pod_info"))
}

func TestFetchMetricsQuery_UserPrometheusScopesEverySelectorAndExpandsTheToken(t *testing.T) {
	recorder := stubUserPrometheus(t, `cluster="prod"`, promVectorResponse)

	out, err := FetchMetricsQuery(testRequestContext(), FetchMetricsRequest{
		AccountId:      "acc",
		MetricProvider: "prometheus",
		Queries: map[string]string{
			"pod_info": `kube_pod_info{pod_ip=~"10.0.0.1"}`,
			"apps":     `group by (destination) ({ __CLUSTER__ __name__="container_postgres_queries_total"})`,
		},
		EndTime: time.Now().UnixMilli(),
		Instant: true,
	})
	require.NoError(t, err)
	require.Len(t, out.Results, 2)
	assert.Equal(t, "api-abc", PromQLLabels(out, "pod_info")[0]["created_by_name"])

	for _, req := range recorder.all() {
		query := req.URL.Query().Get("query")
		assert.Contains(t, query, `cluster="prod"`, "every query is pinned to the account's cluster: %s", query)
		assert.NotContains(t, query, "__CLUSTER__")
		assert.Equal(t, "/api/v1/query", req.URL.Path)
	}
}

func TestFetchMetricsQuery_UserPrometheusRangeReturnsTypedSeries(t *testing.T) {
	recorder := stubUserPrometheus(t, "", promMatrixResponse)

	end := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	out, err := FetchMetricsQuery(testRequestContext(), FetchMetricsRequest{
		AccountId: "acc", MetricProvider: "prometheus",
		Queries:   map[string]string{"cpu": `sum(rate(container_cpu_usage_seconds_total{pod=~"api.*"}[5m]))`},
		StartTime: end.Add(-time.Hour).UnixMilli(), EndTime: end.UnixMilli(), StepInterval: 30,
	})
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	series := out.Results[0].Payload
	require.Len(t, series, 1)
	assert.Equal(t, []int64{1700000000, 1700000060}, series[0].Timestamps, "unix seconds")
	assert.Equal(t, []float64{1, 2.5}, series[0].Values)
	assert.Equal(t, "api-1", series[0].Metric["pod"])

	req := recorder.all()[0]
	assert.Equal(t, "/api/v1/query_range", req.URL.Path)
	assert.Equal(t, "30", req.URL.Query().Get("step"))
	assert.Equal(t, `sum(rate(container_cpu_usage_seconds_total{pod=~"api.*"}[5m]))`, req.URL.Query().Get("query"), "no labels configured: the query is sent as written")
}

// stubUtilisationProvider fixes which provider FetchMetricUtilisation builds queries
// for, in place of the account's integration rows.
func stubUtilisationProvider(t *testing.T, provider, source string) {
	t.Helper()
	orig := utilisationProviderForAccount
	utilisationProviderForAccount = func(*security.RequestContext, string, string, string, string) (string, string, error) {
		return provider, source, nil
	}
	t.Cleanup(func() { utilisationProviderForAccount = orig })
}

// fakeMetricSource stands in for a non-Prometheus provider: it records the native
// queries it is handed and answers only the keys it is given series for, which is how
// a real source behaves for a key its builder has no query for.
type fakeMetricSource struct {
	MetricSource
	answers map[string][]Result
	queries map[string]string
}

func (f *fakeMetricSource) FetchMetricsQuery(_ *security.RequestContext, req FetchMetricsRequest) (OutputMetricQuery, error) {
	f.queries = req.Queries
	out := OutputMetricQuery{}
	for key, query := range req.Queries {
		out.Results = append(out.Results, QueryResult{QueryKey: key, Query: query, Payload: f.answers[key]})
	}
	return out, nil
}

func stubMetricSource(t *testing.T, source MetricSource) {
	t.Helper()
	orig := metricsSourceForAccount
	metricsSourceForAccount = func(*security.RequestContext, string, string, string) (MetricSource, error) {
		return source, nil
	}
	t.Cleanup(func() { metricsSourceForAccount = orig })
}

func TestWorkloadMetricEvidence_RendersThePersistedCard(t *testing.T) {
	stubUtilisationProvider(t, "prometheus", "user")
	recorder := stubUserPrometheus(t, `cluster="prod"`, promMatrixResponse)

	end := time.Now().UTC()
	card, err := WorkloadMetricEvidence(testRequestContext(), "acc", "api", "shop", "Deployment", "cpu", "Cpu Metric", end.Add(-time.Hour), end)
	require.NoError(t, err)

	assert.Equal(t, "prometheus", card["type"])
	assert.Equal(t, "Cpu Metric", card["additional_info"].(map[string]any)["title"])
	assert.Equal(t, "1.0", card["metadata"].(map[string]any)["query-result-version"])
	assert.Empty(t, card["insight"])

	// Prometheus has the per-pod key, so the card keeps one line per pod. The
	// selection is the utilisation layer's: container!="" is what stops each pod
	// being counted twice, and "api-" is what stops workload "api" matching
	// "api-gateway"'s pods.
	shown := card["metadata"].(map[string]any)["query"].(string)
	assert.Contains(t, shown, `sum by (pod, namespace) (rate(container_cpu_usage_seconds_total{`)
	assert.Contains(t, shown, `pod=~"api-.*"`)
	assert.Contains(t, shown, `container!=""`)

	data := card["data"].(map[string]any)
	assert.Equal(t, "matrix", data["result_type"])
	series := data["series_list_result"].([]any)
	require.Len(t, series, 1)
	first := series[0].(map[string]any)
	assert.Equal(t, []any{1.7e9, 1.70000006e9}, first["timestamps"], "float seconds, as the agent emits")
	assert.Equal(t, []any{"1", "2.5"}, first["values"], "strings, as the agent emits")

	for _, req := range recorder.all() {
		query := req.URL.Query().Get("query")
		assert.Contains(t, query, `cluster="prod"`)
		assert.Contains(t, query, `namespace="shop"`)
	}

	_, err = WorkloadMetricEvidence(testRequestContext(), "acc", "api", "shop", "Deployment", "disk", "Disk", end.Add(-time.Hour), end)
	assert.Error(t, err, "unknown workload metric")
}

// The account's default provider answers, not Prometheus. Datadog has no per-pod key,
// so the card falls to the workload total; it reports CPU in nanocores, which the
// utilisation layer turns into cores so the number means the same on every provider.
func TestWorkloadMetricEvidence_IsAnsweredByTheDefaultProvider(t *testing.T) {
	stubUtilisationProvider(t, "datadog", "user")
	source := &fakeMetricSource{answers: map[string][]Result{
		"cpu_usage": {{
			Metric:     map[string]string{"kube_stateful_set": "loki", "unit": "nanocore"},
			Timestamps: []int64{1700000000},
			Values:     []float64{2_500_000_000},
		}},
	}}
	stubMetricSource(t, source)

	end := time.Now().UTC()
	card, err := WorkloadMetricEvidence(testRequestContext(), "acc", "loki", "loki", "StatefulSet", "cpu", "Cpu Metric", end.Add(-time.Hour), end)
	require.NoError(t, err)

	// The kind reaches the builder: a StatefulSet is filtered by its own tag.
	assert.Equal(t, "avg:kubernetes.cpu.usage.total{kube_namespace:loki, kube_stateful_set:loki} by {kube_stateful_set}",
		source.queries["cpu_usage"])
	assert.NotContains(t, source.queries, "cpu_usage_by_pod", "Datadog has no per-pod key")

	assert.Equal(t, source.queries["cpu_usage"], card["metadata"].(map[string]any)["query"],
		"the card shows the provider's own query")
	series := card["data"].(map[string]any)["series_list_result"].([]any)
	require.Len(t, series, 1)
	first := series[0].(map[string]any)
	assert.Equal(t, []any{"2.5"}, first["values"], "nanocores become cores")
	assert.Equal(t, "core", first["metric"].(map[string]any)["unit"])
}

// A provider with no equivalent for a metric answers nothing, and the card carries no
// series — which the investigate page hides. There is no fallback to Prometheus.
func TestWorkloadMetricEvidence_IsEmptyWhereTheProviderHasNoSuchMetric(t *testing.T) {
	stubUtilisationProvider(t, "solarwinds", "user")
	source := &fakeMetricSource{}
	stubMetricSource(t, source)

	end := time.Now().UTC()
	card, err := WorkloadMetricEvidence(testRequestContext(), "acc", "api", "shop", "Deployment", "cpu_throttling", "Cpu Throttling Metric", end.Add(-time.Hour), end)
	require.NoError(t, err)

	assert.Empty(t, source.queries, "SolarWinds has no throttling query, so nothing is asked")
	data := card["data"].(map[string]any)
	assert.Equal(t, "matrix", data["result_type"])
	assert.Empty(t, data["series_list_result"])
}

// A provider the utilisation layer has no builder for at all is an error; both callers
// skip a failed metric, so the event simply carries no such card. Azure App Insights
// rather than CubeAPM or Splunk Enterprise, which the utilisation layer now serves.
func TestWorkloadMetricEvidence_FailsForAProviderWithNoUtilisationSupport(t *testing.T) {
	stubUtilisationProvider(t, "azure_app_insights", "user")

	end := time.Now().UTC()
	_, err := WorkloadMetricEvidence(testRequestContext(), "acc", "api", "shop", "Deployment", "cpu", "Cpu Metric", end.Add(-time.Hour), end)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "azure_app_insights")
}

// Only series Datadog labels as nanocores are scaled: memory, and CPU requests and
// limits that Datadog already reports in cores, must pass through untouched.
func TestDatadogNanocoresToCores(t *testing.T) {
	results := []QueryResult{
		{QueryKey: "cpu_usage", Payload: []Result{{Metric: map[string]string{"unit": "nanocore"}, Values: []float64{1_500_000_000, 250_000_000}}}},
		{QueryKey: "cpu_request", Payload: []Result{{Metric: map[string]string{"unit": "core"}, Values: []float64{0.5}}}},
		{QueryKey: "memory_usage", Payload: []Result{{Metric: map[string]string{"unit": "byte"}, Values: []float64{1024}}}},
		{QueryKey: "no_unit", Payload: []Result{{Metric: map[string]string{}, Values: []float64{7}}}},
	}

	datadogNanocoresToCores(results, RequestMetadata{})

	assert.Equal(t, []float64{1.5, 0.25}, results[0].Payload[0].Values)
	assert.Equal(t, "core", results[0].Payload[0].Metric["unit"])
	assert.Equal(t, []float64{0.5}, results[1].Payload[0].Values)
	assert.Equal(t, []float64{1024}, results[2].Payload[0].Values)
	assert.Equal(t, []float64{7}, results[3].Payload[0].Values)
}

func TestPromQLCardEncoders_MatchTheAgentShapesThePlaybookParsersRead(t *testing.T) {
	result := QueryResult{QueryKey: "A", Payload: []Result{
		{Metric: map[string]string{"pod": "api-1"}, Timestamps: []int64{1700000000, 1700000060}, Values: []float64{1, 2.5}},
		{Metric: map[string]string{"pod": "empty"}},
	}}

	instant := promQLQueriesResult(result, true).([]any)
	require.Len(t, instant, 1, "series without samples are dropped")
	assert.Equal(t, []any{1.7e9, "1"}, instant[0].(map[string]any)["value"], "instant queries_enricher keeps the [ts, value] tuple")
	assert.Equal(t, 1.0, playbooks.LatestValueEntries(promQLQueriesResult(result, true))[0].Value)

	vector := promQLSingleEnvelope(result, true)
	assert.Equal(t, "vector", vector["result_type"])
	entry := vector["vector_result"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"timestamp": 1.7e9, "value": "1"}, entry["value"], "prometheus_enricher keeps the object form")

	matrix := promQLSingleEnvelope(result, false)
	assert.Equal(t, "matrix", matrix["result_type"])
	assert.Equal(t, 2.5, playbooks.LatestValueEntries(matrix)[0].Value, "the playbooks parsers read the range envelope")

	msg := "parse error"
	errEnv := promQLQueriesResult(QueryResult{QueryKey: "A", Error: &msg}, false).(map[string]any)
	assert.Equal(t, "error", errEnv["result_type"])
	assert.Equal(t, "parse error", errEnv["string_result"])
}

func TestPromQLStepSeconds(t *testing.T) {
	assert.Equal(t, 30, promQLStepSeconds("30s"))
	assert.Equal(t, 60, promQLStepSeconds("60"))
	assert.Equal(t, 300, promQLStepSeconds("5m"))
	assert.Equal(t, 0, promQLStepSeconds(""))
	assert.Equal(t, 0, promQLStepSeconds("soon"))
}

func TestFetchMetricsQuery_RefusesAContextWithoutATenant(t *testing.T) {
	// NewRequestContextForTenantAdmin leaves the security context nil when the
	// tenant lookup fails; the resolver must refuse rather than dereference it
	// inside an evidence goroutine.
	_, err := FetchMetricsQuery(&security.RequestContext{}, FetchMetricsRequest{AccountId: "acc", MetricProvider: "prometheus", Queries: map[string]string{"A": "up"}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoTenantContext), err)

	_, err = FetchMetricsQuery(nil, FetchMetricsRequest{AccountId: "acc", MetricProvider: "prometheus"})
	assert.True(t, errors.Is(err, ErrNoTenantContext))

	// The guard lives in the shared resolver, so the entry points that bypass
	// FetchMetricsQuery — the provider lookup the enrichers make first, and the
	// utilisation builders the OOM card uses — refuse the same way.
	_, _, err = GetLogsMetricsTracesProvider(&security.RequestContext{}, "acc", "", "metrics", "")
	assert.True(t, errors.Is(err, ErrNoTenantContext), err)
	_, err = FetchMetricUtilisation(&security.RequestContext{}, GetUtilisationTrendRequest{AccountId: "acc"})
	assert.True(t, errors.Is(err, ErrNoTenantContext), err)
}
