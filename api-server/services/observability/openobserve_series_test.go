package observability

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"nudgebee/services/integrations"
	"nudgebee/services/security"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openObserveSeriesServer stands in for OpenObserve: it serves the metric stream
// schemas and answers each UNION search from an in-memory series table, recording the
// SQL it was asked so the tests can assert on how the query was built.
func openObserveSeriesServer(t *testing.T, schemas map[string][]string, series []map[string]string) (*httptest.Server, *[]string) {
	t.Helper()

	var mu sync.Mutex
	sqls := []string{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/test-org/streams", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "metrics", r.URL.Query().Get("type"))
		assert.Equal(t, "true", r.URL.Query().Get("fetchSchema"))

		type field struct {
			Name string `json:"name"`
		}
		type stream struct {
			Name   string  `json:"name"`
			Schema []field `json:"schema"`
		}
		out := struct {
			List []stream `json:"list"`
		}{}
		for name, cols := range schemas {
			s := stream{Name: name}
			for _, c := range cols {
				s.Schema = append(s.Schema, field{Name: c})
			}
			out.List = append(out.List, s)
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/test-org/_search", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query struct {
				SQL string `json:"sql"`
			} `json:"query"`
		}
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		sqls = append(sqls, req.Query.SQL)
		mu.Unlock()

		// Resolve each UNION branch against the fake series table: a branch matches when
		// some series carries that stream's __name__ and satisfies every equality /
		// prefix predicate the branch spelled out.
		hits := []map[string]any{}
		for _, branch := range strings.Split(req.Query.SQL, " UNION ALL ") {
			stream := between(branch, `FROM "`, `"`)
			for _, s := range series {
				if s["__name__"] != stream {
					continue
				}
				if branchMatches(branch, s) {
					hits = append(hits, map[string]any{"__name__": stream})
					break
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits, "total": len(hits)})
	})

	server := httptest.NewServer(mux)
	return server, &sqls
}

func between(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// branchMatches evaluates the WHERE predicates of one UNION branch against a series.
func branchMatches(branch string, s map[string]string) bool {
	where := between(branch, " WHERE ", " GROUP BY")
	for _, pred := range strings.Split(where, " AND ") {
		pred = strings.TrimSpace(pred)
		switch {
		case strings.Contains(pred, " LIKE "):
			label := strings.TrimSpace(pred[:strings.Index(pred, " LIKE ")])
			lit := between(pred, "'", "'")
			if !strings.HasPrefix(s[label], strings.TrimSuffix(lit, "%")) {
				return false
			}
		case strings.Contains(pred, " = "):
			label := strings.TrimSpace(pred[:strings.Index(pred, " = ")])
			if s[label] != between(pred, "'", "'") {
				return false
			}
		}
	}
	return true
}

func patchOpenObserveSeriesConfig(t *testing.T, url string) {
	t.Helper()
	patches := gomonkey.NewPatches()
	t.Cleanup(patches.Reset)
	patches.ApplyFunc(integrations.GetOpenObserveConfigs, func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
		return integrations.OpenObserveConfig{URL: url, OrgID: "test-org", Username: "user", Password: "pass"}, nil
	})
}

// The schemas/series below mirror what a real cluster emits: cAdvisor families keyed by
// namespace+pod (pod carries a replica suffix), eBPF families keyed by
// destination_workload_*, and a custom app family keyed by namespace+job.
func seriesFixture() (map[string][]string, []map[string]string) {
	schemas := map[string][]string{
		"container_memory_usage_bytes":      {"__name__", "namespace", "pod", "container"},
		"container_cpu_usage_seconds_total": {"__name__", "namespace", "pod", "container"},
		"container_http_requests_total":     {"__name__", "destination_workload_namespace", "destination_workload_name"},
		"nb_checkout_orders_total":          {"__name__", "namespace", "job"},
	}
	series := []map[string]string{
		{"__name__": "container_memory_usage_bytes", "namespace": "prod", "pod": "checkout-7d9f8b-abcde", "container": "checkout"},
		{"__name__": "container_cpu_usage_seconds_total", "namespace": "prod", "pod": "checkout-7d9f8b-abcde", "container": "checkout"},
		{"__name__": "container_memory_usage_bytes", "namespace": "prod", "pod": "payments-abc123", "container": "payments"},
		{"__name__": "container_http_requests_total", "destination_workload_namespace": "prod", "destination_workload_name": "checkout"},
		{"__name__": "nb_checkout_orders_total", "namespace": "prod", "job": "checkout"},
	}
	return schemas, series
}

// A workload commonly emits under several label conventions at once, and each family
// must be reported against the convention that actually found it.
func TestOpenObserveFetchMetricSeriesGroupsByCandidate(t *testing.T) {
	schemas, series := seriesFixture()
	server, _ := openObserveSeriesServer(t, schemas, series)
	defer server.Close()
	patchOpenObserveSeriesConfig(t, server.URL)

	out, err := (&OpenObserveMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err)

	got := map[string][]string{}
	for _, m := range out.Matches {
		got[m.WorkloadLabel] = m.Families
	}

	assert.Equal(t, []string{"container_http_requests_total"}, got["destination_workload_name"])
	assert.Equal(t, []string{"nb_checkout_orders_total"}, got["job"])
	assert.Equal(t, []string{"container_cpu_usage_seconds_total", "container_memory_usage_bytes"}, got["pod"],
		"pod is a PREFIX match: the replica suffix must not prevent the match")
}

// The whole point of series-match is that it discriminates. A family that only has
// series for a DIFFERENT workload must not be reported for this one.
func TestOpenObserveFetchMetricSeriesExcludesOtherWorkloads(t *testing.T) {
	schemas, series := seriesFixture()
	server, _ := openObserveSeriesServer(t, schemas, series)
	defer server.Close()
	patchOpenObserveSeriesConfig(t, server.URL)

	out, err := (&OpenObserveMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "payments",
	})
	require.NoError(t, err)

	for _, m := range out.Matches {
		assert.NotContains(t, m.Families, "nb_checkout_orders_total")
		assert.NotContains(t, m.Families, "container_http_requests_total")
	}
}

func TestOpenObserveFetchMetricSeriesUnknownWorkloadIsEmptyNotError(t *testing.T) {
	schemas, series := seriesFixture()
	server, _ := openObserveSeriesServer(t, schemas, series)
	defer server.Close()
	patchOpenObserveSeriesConfig(t, server.URL)

	out, err := (&OpenObserveMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "does-not-exist",
	})
	require.NoError(t, err)
	assert.Empty(t, out.Matches)
}

// A stream that lacks the candidate's column must be left out of the statement: OpenObserve
// fails the WHOLE union with "unknown field" otherwise, taking the streams that would have
// matched down with it. This is correctness, not just efficiency.
func TestOpenObserveFetchMetricSeriesPrunesStreamsMissingTheLabel(t *testing.T) {
	schemas, series := seriesFixture()
	server, sqls := openObserveSeriesServer(t, schemas, series)
	defer server.Close()
	patchOpenObserveSeriesConfig(t, server.URL)

	_, err := (&OpenObserveMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err)

	for _, sql := range *sqls {
		if strings.Contains(sql, "pod LIKE") {
			assert.NotContains(t, sql, `FROM "container_http_requests_total"`,
				"a stream without a pod column must not be unioned into a pod query")
			assert.NotContains(t, sql, `FROM "nb_checkout_orders_total"`)
		}
		if strings.Contains(sql, "destination_workload_name =") {
			assert.NotContains(t, sql, `FROM "container_memory_usage_bytes"`)
		}
	}
}

// A workload name containing LIKE wildcards must match literally rather than widening
// the filter to everything.
func TestOpenObserveSeriesWhereEscapesLikeWildcards(t *testing.T) {
	where := openObserveSeriesWhere(
		seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "pod", MatchType: matchTypePrefix},
		"prod", "web_100%")

	assert.Contains(t, where, `\_`, "underscore must be escaped")
	assert.Contains(t, where, `\%`, "percent must be escaped")
	assert.Contains(t, where, `ESCAPE '\'`)
}

func TestOpenObserveSeriesWhereEscapesQuotes(t *testing.T) {
	where := openObserveSeriesWhere(
		seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "job", MatchType: matchTypeExact},
		"pro'd", "chec'kout")

	assert.Contains(t, where, "'pro''d'")
	assert.Contains(t, where, "'chec''kout'")
}

// Reached through the optional-capability type assertion in FetchMetricSeries, so the
// interface has to be satisfied on the pointer receiver getMetricsSource returns.
func TestOpenObserveImplementsMetricSeriesSource(t *testing.T) {
	src, err := getMetricsSource("openobserve", "user")
	require.NoError(t, err)
	_, ok := src.(MetricSeriesSource)
	assert.True(t, ok, "openobserve must satisfy MetricSeriesSource so metrics_series_match resolves")
}

// Backend unreachable must surface, not read back as "this workload has no metrics".
func TestOpenObserveFetchMetricSeriesSurfacesBackendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"message":"boom"}`))
	}))
	defer server.Close()
	patchOpenObserveSeriesConfig(t, server.URL)

	_, err := (&OpenObserveMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.Error(t, err)
}

// A caller-supplied limit must bound what comes back AND be reported as truncated, so
// the agent knows it is looking at part of the catalogue rather than all of it.
func TestOpenObserveFetchMetricSeriesHonoursLimit(t *testing.T) {
	schemas, series := seriesFixture()
	server, _ := openObserveSeriesServer(t, schemas, series)
	defer server.Close()
	patchOpenObserveSeriesConfig(t, server.URL)

	out, err := (&OpenObserveMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout", Limit: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Matches)

	for _, m := range out.Matches {
		assert.LessOrEqual(t, len(m.Families), 1)
	}
	assert.True(t, out.Truncated, "a limited result must say so, or the agent reads it as the whole catalogue")
}

// The sweep divides one action deadline across several searches. Once it is spent, the
// lookup must report a partial result rather than keep issuing requests the handler's
// 30s timeout will kill anyway.
func TestOpenObserveSeriesFamiliesStopsWhenBudgetSpent(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"hits":[],"total":0}`))
	}))
	defer server.Close()

	cfg := integrations.OpenObserveConfig{URL: server.URL, OrgID: "test-org", Username: "u", Password: "p"}
	candidate := seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "pod", MatchType: matchTypePrefix}

	found, stopped, err := openObserveSeriesFamilies(cfg, candidate, "prod", "checkout",
		[]string{"container_memory_usage_bytes"}, 0, 0, seriesMatchDefaultLimit, time.Now().Add(-time.Second))

	require.NoError(t, err, "an exhausted budget is a partial answer, not a failure")
	assert.True(t, stopped, "stopping early must be reported so the result is flagged truncated")
	assert.Empty(t, found)
	assert.Zero(t, atomic.LoadInt32(&calls), "no search should be issued once the budget is gone")
}

// Enough families in hand means the remaining chunks cannot change the answer, so they
// must not cost requests.
func TestOpenObserveSeriesFamiliesStopsOnceLimitReached(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"hits":[{"__name__":"container_memory_usage_bytes"}],"total":1}`))
	}))
	defer server.Close()

	cfg := integrations.OpenObserveConfig{URL: server.URL, OrgID: "test-org", Username: "u", Password: "p"}
	candidate := seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "pod", MatchType: matchTypePrefix}

	streams := make([]string, openObserveSeriesMaxStreamsPerQuery*3)
	for i := range streams {
		streams[i] = fmt.Sprintf("metric_%03d", i)
	}

	found, stopped, err := openObserveSeriesFamilies(cfg, candidate, "prod", "checkout",
		streams, 0, 0, 1, time.Now().Add(openObserveSeriesBudget))

	require.NoError(t, err)
	assert.True(t, stopped)
	assert.Len(t, found, 1)
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls), "the limit was met by the first chunk; the other two must not be queried")
}
