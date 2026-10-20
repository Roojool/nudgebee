package observability

// Tests for the Splunk Enterprise series-match primitive. The stub resolves each
// mcatalog WHERE clause against an in-memory series table the way Splunk does — a
// dimension the series does not carry simply fails to match, rather than erroring —
// so the assertions are about which conventions find which families, not about SPL
// string equality.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"nudgebee/services/integrations"
	"nudgebee/services/security"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reached through the optional-capability type assertion in FetchMetricSeries, so the
// interface has to be satisfied on the pointer receiver getMetricsSource returns.
func TestSplunkEnterpriseImplementsMetricSeriesSource(t *testing.T) {
	src, err := getMetricsSource("splunk_enterprise", "user")
	require.NoError(t, err)
	_, ok := src.(MetricSeriesSource)
	assert.True(t, ok, "splunk_enterprise must satisfy MetricSeriesSource so metrics_series_match resolves")
}

// splunkSeriesFixture mirrors the live seed: a checkout workload emitting under three
// conventions, plus a payments workload sharing one family that must not leak into
// checkout's answer. Dimension names are the OTel spellings Splunk actually stores.
func splunkSeriesFixture() []map[string]string {
	return []map[string]string{
		{"metric_name": "k8s.pod.cpu.usage", "k8s.namespace.name": "prod",
			"k8s.pod.name": "checkout-7d9f8b-abcde", "k8s.container.name": "checkout"},
		{"metric_name": "k8s.pod.memory.usage", "k8s.namespace.name": "prod",
			"k8s.pod.name": "checkout-7d9f8b-abcde", "k8s.container.name": "checkout"},
		{"metric_name": "k8s.pod.cpu.usage", "k8s.namespace.name": "prod",
			"k8s.pod.name": "payments-abc123", "k8s.container.name": "payments"},
		{"metric_name": "nb_checkout_orders_total", "k8s.namespace.name": "prod",
			"k8s.deployment.name": "checkout"},
		{"metric_name": "nb_mesh_requests_total",
			"destination_workload_namespace": "prod", "destination_workload_name": "checkout"},
	}
}

// splunkSeriesServer stands in for Splunk's oneshot search endpoint, recording every SPL
// it was asked so tests can assert on how many searches the sweep actually issued.
func splunkSeriesServer(t *testing.T, series []map[string]string) (*httptest.Server, *[]string) {
	t.Helper()

	var mu sync.Mutex
	searches := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// t.Errorf, not require: this runs on the server's goroutine, and
		// require calls t.FailNow, which is only safe on the test goroutine.
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		spl := r.Form.Get("search")

		mu.Lock()
		searches = append(searches, spl)
		mu.Unlock()

		names := map[string]bool{}
		for _, s := range series {
			if splunkWhereMatches(spl, s) {
				names[s["metric_name"]] = true
			}
		}

		values := []any{}
		for n := range names {
			values = append(values, n)
		}

		results := []map[string]any{}
		if len(values) > 0 {
			results = append(results, map[string]any{"values(metric_name)": values})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))

	return server, &searches
}

// splunkWhereMatches evaluates an mcatalog WHERE clause against one series. A predicate
// naming a dimension the series lacks simply does not match — Splunk's behaviour, and
// the reason this source needs no schema pruning.
func splunkWhereMatches(spl string, series map[string]string) bool {
	where := spl
	if i := strings.Index(spl, " WHERE "); i >= 0 {
		where = spl[i+len(" WHERE "):]
	}
	for _, pred := range strings.Split(where, " AND ") {
		pred = strings.TrimSpace(pred)
		eq := strings.Index(pred, "=")
		if eq < 0 {
			return false
		}
		field := strings.TrimSpace(pred[:eq])
		value := strings.Trim(strings.TrimSpace(pred[eq+1:]), `"`)
		if field == "index" {
			continue
		}
		got, ok := series[field]
		if !ok {
			return false
		}
		if strings.HasSuffix(value, "*") {
			if !strings.HasPrefix(got, strings.TrimSuffix(value, "*")) {
				return false
			}
			continue
		}
		if got != value {
			return false
		}
	}
	return true
}

func patchSplunkSeriesConfig(t *testing.T, serverURL string) {
	t.Helper()
	patches := gomonkey.NewPatches()
	t.Cleanup(patches.Reset)
	patches.ApplyFunc(integrations.GetSplunkEnterpriseConfig,
		func(_ *security.RequestContext, _ string) (integrations.SplunkEnterpriseConfig, error) {
			return integrations.SplunkEnterpriseConfig{
				URL:         serverURL,
				AuthType:    integrations.SplunkEnterpriseAuthToken,
				Token:       "tok",
				MetricIndex: "otel_metrics",
				App:         integrations.SplunkEnterpriseDefaultApp,
			}, nil
		})
}

// A workload commonly emits under several conventions at once, and each family must be
// reported against the convention that actually found it.
func TestSplunkEnterpriseFetchMetricSeriesGroupsByCandidate(t *testing.T) {
	server, _ := splunkSeriesServer(t, splunkSeriesFixture())
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err)

	got := map[string][]string{}
	for _, m := range out.Matches {
		got[m.WorkloadLabel] = m.Families
	}

	assert.Equal(t, []string{"nb_mesh_requests_total"}, got["destination_workload_name"])
	assert.Equal(t, []string{"nb_checkout_orders_total"}, got["k8s.deployment.name"],
		"k8s.deployment.name is what the `workload` convention maps to")
	assert.Equal(t, []string{"k8s.pod.cpu.usage", "k8s.pod.memory.usage"}, got["k8s.container.name"])
	assert.Equal(t, []string{"k8s.pod.cpu.usage", "k8s.pod.memory.usage"}, got["k8s.pod.name"],
		"pod is a PREFIX match: the replica suffix must not prevent the match")
}

// The agent writes its follow-up mstats from the labels a match reports. Canonical names
// (`pod`, `workload`) are not Splunk dimensions, so reporting them sends that query to a
// field no series carries — an empty result for a workload discovery just found.
func TestSplunkEnterpriseFetchMetricSeriesReportsFilteredDimensions(t *testing.T) {
	series := splunkSeriesFixture()
	server, _ := splunkSeriesServer(t, series)
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Matches)

	for _, m := range out.Matches {
		assert.NotContains(t, splunkEnterpriseMetricLabelMapping, m.NamespaceLabel,
			"%q is a canonical name, not the dimension Splunk filtered on", m.NamespaceLabel)
		assert.NotContains(t, splunkEnterpriseMetricLabelMapping, m.WorkloadLabel,
			"%q is a canonical name, not the dimension Splunk filtered on", m.WorkloadLabel)

		// Round trip: a filter written from the reported labels, the way the agent writes
		// it, must find every family the match reported.
		value := `"checkout"`
		if m.MatchType == matchTypePrefix {
			value = `"checkout*"`
		}
		where := fmt.Sprintf(`index="otel_metrics" AND %s="prod" AND %s=%s`, m.NamespaceLabel, m.WorkloadLabel, value)
		for _, family := range m.Families {
			found := false
			for _, s := range series {
				if s["metric_name"] == family && splunkWhereMatches(where, s) {
					found = true
				}
			}
			assert.True(t, found, "%s is not reachable through the reported labels (%s)", family, where)
		}
	}
}

// The OTel conventions have to be searched before the eBPF/scrape ones an OTel index
// rarely carries, or the sweep's budget is spent on likely misses first. This is also
// the one Splunk-specific view of the shared table, so it must not disturb that table.
func TestSplunkEnterpriseSeriesCandidatesPutOTelDimensionsFirst(t *testing.T) {
	shared := append([]seriesMatchCandidate(nil), SeriesMatchCandidates...)

	candidates := splunkEnterpriseSeriesCandidates()

	assert.Equal(t, shared, SeriesMatchCandidates,
		"the shared table feeds the Prometheus and OpenObserve sources and must not be reordered or rewritten")
	require.Len(t, candidates, len(SeriesMatchCandidates), "reordered, not pruned")

	otel := map[string]bool{}
	for _, dim := range splunkEnterpriseMetricLabelMapping {
		otel[dim] = true
	}
	seenRaw := false
	for _, c := range candidates {
		if !otel[c.WorkloadLabel] {
			seenRaw = true
			continue
		}
		assert.False(t, seenRaw, "%s is queued behind a convention with no OTel dimension", c.WorkloadLabel)
	}
	assert.Contains(t, candidates, seriesMatchCandidate{
		NamespaceLabel: "k8s.namespace.name", WorkloadLabel: "k8s.pod.name", MatchType: matchTypePrefix,
	}, "the pod convention keeps its prefix match once resolved")
}

// The whole point of series-match is that it discriminates. A family that only has
// series for a DIFFERENT workload must not be reported for this one.
func TestSplunkEnterpriseFetchMetricSeriesExcludesOtherWorkloads(t *testing.T) {
	server, _ := splunkSeriesServer(t, splunkSeriesFixture())
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "payments",
	})
	require.NoError(t, err)

	for _, m := range out.Matches {
		assert.NotContains(t, m.Families, "nb_checkout_orders_total")
		assert.NotContains(t, m.Families, "nb_mesh_requests_total")
		assert.NotContains(t, m.Families, "k8s.pod.memory.usage")
	}
}

func TestSplunkEnterpriseFetchMetricSeriesUnknownWorkloadIsEmptyNotError(t *testing.T) {
	server, _ := splunkSeriesServer(t, splunkSeriesFixture())
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "does-not-exist",
	})
	require.NoError(t, err)
	assert.Empty(t, out.Matches)
	assert.False(t, out.Truncated,
		"every convention was searched, so this empty is complete — what lets the tool say no series exist")
}

// `app` and `workload` both map to k8s.deployment.name, so they render an identical
// filter. Issuing both would cost a duplicate search and report the same families under
// two headings.
func TestSplunkEnterpriseFetchMetricSeriesDedupesCollapsedConventions(t *testing.T) {
	server, searches := splunkSeriesServer(t, splunkSeriesFixture())
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, spl := range *searches {
		assert.False(t, seen[spl], "the same filter must not be searched twice: %s", spl)
		seen[spl] = true
	}
	assert.Len(t, *searches, len(SeriesMatchCandidates)-1,
		"exactly one candidate pair collapses onto another (app -> workload)")

	deployment := 0
	for _, m := range out.Matches {
		if m.WorkloadLabel == "k8s.deployment.name" {
			deployment++
		}
	}
	assert.Equal(t, 1, deployment, "the deduped convention must not appear as its own group")
}

// Discovery must filter server-side. A WHERE clause that dropped the workload predicate
// would return the whole catalogue and read as "everything belongs to this workload".
func TestSplunkEnterpriseSeriesWhereFiltersOnBothDimensions(t *testing.T) {
	where, err := splunkEnterpriseSeriesWhere(
		seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "pod", MatchType: matchTypePrefix},
		"otel_metrics", "prod", "checkout")
	require.NoError(t, err)

	assert.Equal(t, `index="otel_metrics" AND k8s.namespace.name="prod" AND k8s.pod.name="checkout*"`, where,
		"canonical labels must be mapped to OTel dimensions and the pod match must be a prefix")
}

// An omitted namespace must widen the search, not produce a predicate against an empty
// string that matches nothing.
func TestSplunkEnterpriseSeriesWhereOmitsEmptyNamespace(t *testing.T) {
	where, err := splunkEnterpriseSeriesWhere(
		seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "container", MatchType: matchTypeExact},
		"otel_metrics", "", "checkout")
	require.NoError(t, err)

	assert.Equal(t, `index="otel_metrics" AND k8s.container.name="checkout"`, where)
	assert.NotContains(t, where, `k8s.namespace.name=""`)
}

// A workload value is quoted with its quote escaped, so it cannot break out of the
// literal. A '*' in it stays a wildcard: Splunk honours no escape for '*' inside a quoted
// term, so no rendering can make it literal, and Kubernetes names cannot contain one.
func TestSplunkEnterpriseSeriesWhereQuotesValues(t *testing.T) {
	exact, err := splunkEnterpriseSeriesWhere(
		seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "container", MatchType: matchTypeExact},
		"otel_metrics", "prod", `we*b"x`)
	require.NoError(t, err)
	assert.Contains(t, exact, `k8s.container.name="we*b\"x"`, "a quote must not close the literal")

	// The prefix form appends its own trailing wildcard.
	prefix, err := splunkEnterpriseSeriesWhere(
		seriesMatchCandidate{NamespaceLabel: "namespace", WorkloadLabel: "pod", MatchType: matchTypePrefix},
		"otel_metrics", "prod", "web")
	require.NoError(t, err)
	assert.Contains(t, prefix, `k8s.pod.name="web*"`)
}

// Splunk unreachable must surface, not read back as "this workload has no metrics".
func TestSplunkEnterpriseFetchMetricSeriesSurfacesBackendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"messages":[{"type":"ERROR","text":"boom"}]}`))
	}))
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	_, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.Error(t, err)
}

// A missing metrics index is a configuration problem, and the error has to say so —
// the agent otherwise reports the workload as uninstrumented.
func TestSplunkEnterpriseFetchMetricSeriesRequiresMetricIndex(t *testing.T) {
	patches := gomonkey.NewPatches()
	defer patches.Reset()
	patches.ApplyFunc(integrations.GetSplunkEnterpriseConfig,
		func(_ *security.RequestContext, _ string) (integrations.SplunkEnterpriseConfig, error) {
			return integrations.SplunkEnterpriseConfig{URL: "http://unused", AuthType: integrations.SplunkEnterpriseAuthToken, Token: "t"}, nil
		})

	_, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metrics index")
}

func TestSplunkEnterpriseFetchMetricSeriesRequiresWorkload(t *testing.T) {
	server, _ := splunkSeriesServer(t, splunkSeriesFixture())
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	_, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod",
	})
	require.Error(t, err)
}

// The sweep divides one action deadline across its searches. Once it is spent the
// lookup must report a partial result rather than keep issuing searches the handler's
// 30s timeout will kill anyway.
func TestSplunkEnterpriseSeriesFamiliesStopsWhenBudgetSpent(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	cfg := integrations.SplunkEnterpriseConfig{
		URL: server.URL, AuthType: integrations.SplunkEnterpriseAuthToken, Token: "tok",
		MetricIndex: "otel_metrics", App: integrations.SplunkEnterpriseDefaultApp,
	}

	found, stopped, err := (&SplunkEnterpriseMetricSource{}).splunkEnterpriseSeriesFamilies(
		mockRequestContext(), cfg, "otel_metrics",
		`index="otel_metrics" AND k8s.namespace.name="prod"`, 0, 0, time.Now().Add(-time.Second))

	require.NoError(t, err, "an exhausted budget is a partial answer, not a failure")
	assert.True(t, stopped, "stopping early must be reported so the result is flagged truncated")
	assert.Empty(t, found)
	assert.Zero(t, calls, "no search should be issued once the budget is gone")
}

// Every candidate failing means the backend is broken; a single convention failing must
// not discard the families the others found.
func TestSplunkEnterpriseFetchMetricSeriesSurvivesPartialFailure(t *testing.T) {
	series := splunkSeriesFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// t.Errorf, not require: this runs on the server's goroutine, and
		// require calls t.FailNow, which is only safe on the test goroutine.
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		spl := r.Form.Get("search")
		if strings.Contains(spl, "k8s.pod.name=") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"messages":[{"type":"ERROR","text":"boom"}]}`))
			return
		}
		names := map[string]bool{}
		for _, s := range series {
			if splunkWhereMatches(spl, s) {
				names[s["metric_name"]] = true
			}
		}
		values := []any{}
		for n := range names {
			values = append(values, n)
		}
		results := []map[string]any{}
		if len(values) > 0 {
			results = append(results, map[string]any{"values(metric_name)": values})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err, "one failing convention must not fail the whole lookup")

	got := map[string][]string{}
	for _, m := range out.Matches {
		got[m.WorkloadLabel] = m.Families
	}
	assert.Equal(t, []string{"nb_checkout_orders_total"}, got["k8s.deployment.name"])
	assert.Empty(t, got["k8s.pod.name"], "the convention whose search failed reports nothing")
	assert.True(t, out.Truncated, "a convention that was never searched makes the answer partial")
}

// A failed search is a convention nobody looked at. If the rest found nothing, the answer
// is "could not tell", not "no metrics" — the agent stops investigating on the latter.
func TestSplunkEnterpriseFetchMetricSeriesFailedCandidateIsNotAnEmptyAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.Contains(r.Form.Get("search"), "k8s.pod.name=") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"messages":[{"type":"ERROR","text":"search timed out"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	patchSplunkSeriesConfig(t, server.URL)

	out, err := (&SplunkEnterpriseMetricSource{}).FetchMetricSeries(mockRequestContext(), FetchMetricSeriesRequest{
		AccountId: "test", Namespace: "prod", Workload: "checkout",
	})
	require.NoError(t, err, "the other conventions answered, so this is a partial result, not a failure")
	assert.Empty(t, out.Matches)
	assert.True(t, out.Truncated, "an empty result with a failed convention must not read as complete")
}
