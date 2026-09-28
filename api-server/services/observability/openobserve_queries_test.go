package observability

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/services/integrations"
	"nudgebee/services/security"
)

// openObserveTestBackend stands in for OpenObserve, answering only the families named in
// `present` and recording every query it was asked. Anything else answers success-with-no-
// series, which is how OpenObserve reports an absent family.
func openObserveTestBackend(t *testing.T, present map[string][]float64) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string

	base := time.Now().Add(-time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		q := r.Form.Get("query")
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()

		var values []float64
		for family, vals := range present {
			if strings.Contains(q, family) {
				values = vals
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if values == nil {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
			return
		}
		var b strings.Builder
		b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[`)
		for i, v := range values {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`[`)
			b.WriteString(strconv.FormatInt(base+int64(i)*60, 10))
			b.WriteString(`,"`)
			b.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
			b.WriteString(`"]`)
		}
		b.WriteString(`]}]}}`)
		_, _ = w.Write([]byte(b.String()))
	}))

	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, seen...)
	}
}

// patchOpenObserve points the source at the stub backend. The metric-source resolver is
// swapped through its package-level var rather than gomonkey: GetLogsMetricsTracesProvider
// is a two-line wrapper the compiler inlines, so a gomonkey patch on it silently no-ops
// under the plain `go test ./...` CI runs (it only holds with -gcflags=all=-l). Same seam
// promql_test.go uses.
func patchOpenObserve(t *testing.T, serverURL string) *gomonkey.Patches {
	t.Helper()
	origResolve := metricsSourceForAccount
	metricsSourceForAccount = func(_ *security.RequestContext, _, _, _ string) (MetricSource, error) {
		return &OpenObserveMetricSource{}, nil
	}
	t.Cleanup(func() { metricsSourceForAccount = origResolve })

	patches := gomonkey.NewPatches()
	patches.ApplyFunc(integrations.GetOpenObserveConfigs, func(_ *security.RequestContext, _ string) (integrations.OpenObserveConfig, error) {
		return integrations.OpenObserveConfig{URL: serverURL, OrgID: "test-org", Username: "user", Password: "pass"}, nil
	})
	return patches
}

// fetchClusterUtilisation drives the OpenObserve branch directly, applying the same
// metadata preparation FetchMetricUtilisation does before dispatching to it.
func fetchClusterUtilisation(t *testing.T, metrics []string) OutputMetricQuery {
	t.Helper()
	now := time.Now()
	req := GetUtilisationTrendRequest{
		AccountId: "test",
		StartTime: now.Add(-time.Hour).UnixMilli(),
		EndTime:   now.UnixMilli(),
		Request:   map[string]any{"metrics": metrics},
	}
	meta, err := parseRequestMetadata(req.Request)
	require.NoError(t, err)
	meta.RangeWindow, meta.Step, meta.RateWindow = promAggWindow(req.StartTime, req.EndTime)

	out, err := fetchOpenObserveUtilisation(mockRequestContext(), req, meta, "user", true)
	require.NoError(t, err)
	return out
}

func resultsByKey(out OutputMetricQuery) map[string]QueryResult {
	m := map[string]QueryResult{}
	for _, r := range out.Results {
		m[r.QueryKey] = r
	}
	return m
}

// The whole reason this file exists: OpenObserve's PromQL engine returns EMPTY for a
// falling-back `or`, for binary vector arithmetic and for quantile(), and 400s on a
// subquery combined with `or`. None of those may appear in what we send it.
func TestOpenObserveClusterQueriesAvoidUnsupportedPromQL(t *testing.T) {
	server, queries := openObserveTestBackend(t, map[string][]float64{
		"machine_cpu_cores":              {84, 84, 84},
		"node_cpu_seconds_total":         {15, 28, 41},
		"node_memory_MemTotal_bytes":     {300, 300, 300},
		"node_memory_MemAvailable_bytes": {210, 200, 190},
	})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	fetchClusterUtilisation(t, []string{
		"cpu_total", "cpu_real", "mem_total", "mem_real",
		"p50_cpu", "p90_cpu", "max_usage_cpu", "p50_mem", "p90_mem", "max_usage_mem",
	})

	sent := queries()
	require.NotEmpty(t, sent)
	for _, q := range sent {
		assert.NotContains(t, q, " or ", "OpenObserve's `or` does not fall back: %s", q)
		assert.NotContains(t, q, "quantile(", "quantile() aggregation returns empty on OpenObserve: %s", q)
		assert.NotContains(t, q, "__CLUSTER__", "placeholder must be expanded: %s", q)
		assert.False(t, strings.Contains(q, "] - ") || strings.Contains(q, "} - "),
			"binary vector arithmetic returns empty on OpenObserve: %s", q)
		assert.NotRegexp(t, `\[\d+s:\d+s\]`, q, "subqueries are unsupported on OpenObserve: %s", q)
	}
}

// Every gauge row the card draws must carry a real figure, derived from the series rather
// than asked of the engine.
func TestOpenObserveClusterAggregatesDeriveEveryCardField(t *testing.T) {
	server, _ := openObserveTestBackend(t, map[string][]float64{
		"machine_cpu_cores":              {84, 84, 84},
		"node_cpu_seconds_total":         {15, 28, 41},
		"node_memory_MemTotal_bytes":     {300, 300, 300},
		"node_memory_MemAvailable_bytes": {210, 200, 190},
	})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	out := fetchClusterUtilisation(t, []string{
		"cpu_total", "cpu_real", "p50_cpu", "p90_cpu", "max_usage_cpu",
		"mem_total", "mem_real", "p50_mem", "p90_mem", "max_usage_mem",
	})
	got := resultsByKey(out)

	for _, key := range []string{"cpu_total", "cpu_real", "p50_cpu", "p90_cpu", "max_usage_cpu",
		"mem_total", "mem_real", "p50_mem", "p90_mem", "max_usage_mem"} {
		r, ok := got[key]
		require.True(t, ok, "%s missing from response", key)
		require.Nil(t, r.Error, "%s errored: %v", key, r.Error)
		require.NotEmpty(t, r.Payload, "%s has no payload", key)
		require.NotEmpty(t, r.Payload[0].Values, "%s has no value", key)
	}

	last := func(k string) float64 {
		v := got[k].Payload[0].Values
		return v[len(v)-1]
	}
	assert.InDelta(t, 84, last("cpu_total"), 0.01, "capacity is the last sample")
	assert.InDelta(t, 28, last("cpu_real"), 0.01, "usage is the window mean, matching rate() over the range")
	assert.InDelta(t, 28, last("p50_cpu"), 0.01)
	assert.InDelta(t, 38.4, last("p90_cpu"), 0.01)
	assert.InDelta(t, 41, last("max_usage_cpu"), 0.01)
	// Memory usage is Total - Available, subtracted in Go: 300-210, 300-200, 300-190.
	assert.InDelta(t, 300, last("mem_total"), 0.01)
	assert.InDelta(t, 110, last("mem_real"), 0.01, "usage is the latest Total - Available")
	assert.InDelta(t, 100, last("p50_mem"), 0.01)
	assert.InDelta(t, 110, last("max_usage_mem"), 0.01)
}

// A family that no collector on this instance publishes must say so. A zeroed gauge is
// indistinguishable from an idle cluster, which is the failure mode this provider invites.
func TestOpenObserveClusterAggregatesReportMissingFamilies(t *testing.T) {
	server, _ := openObserveTestBackend(t, map[string][]float64{})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	out := fetchClusterUtilisation(t, []string{"cpu_total", "mem_real"})
	got := resultsByKey(out)

	require.NotNil(t, got["cpu_total"].Error)
	assert.Contains(t, *got["cpu_total"].Error, "machine_cpu_cores", "the error names what was probed")
	require.NotNil(t, got["mem_real"].Error)
	assert.Contains(t, *got["mem_real"].Error, "node_memory_MemTotal_bytes")
}

// An OTel-fed instance publishes none of the node-exporter spellings; discovery must fall
// through to the kubeletstats families rather than reporting the cluster idle.
func TestOpenObserveClusterAggregatesDiscoverOtelFamilies(t *testing.T) {
	server, _ := openObserveTestBackend(t, map[string][]float64{
		"k8s_node_allocatable_cpu": {64, 64},
		"k8s_node_cpu_usage":       {20, 30},
	})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	got := resultsByKey(fetchClusterUtilisation(t, []string{"cpu_total", "cpu_real"}))
	require.Nil(t, got["cpu_total"].Error)
	assert.InDelta(t, 64, got["cpu_total"].Payload[0].Values[0], 0.01)
	require.Nil(t, got["cpu_real"].Error)
	assert.InDelta(t, 25, got["cpu_real"].Payload[0].Values[0], 0.01)
	assert.Contains(t, got["cpu_real"].Query, "k8s_node_cpu_usage", "the resolved family is reported back")
}

// Workload and node keys are plain sum()/rate() over one selector, which OpenObserve
// evaluates correctly -- they must keep going through the Prometheus builders.
func TestOpenObserveUtilisationPassesNonClusterKeysToPrometheusBuilders(t *testing.T) {
	server, queries := openObserveTestBackend(t, map[string][]float64{
		"kube_pod_container_resource_requests": {117},
	})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	got := resultsByKey(fetchClusterUtilisation(t, []string{"cpu_request"}))
	require.Nil(t, got["cpu_request"].Error)
	assert.InDelta(t, 117, got["cpu_request"].Payload[0].Values[0], 0.01)

	var sawBuilderQuery bool
	for _, q := range queries() {
		if strings.Contains(q, "kube_pod_container_resource_requests") {
			sawBuilderQuery = true
			assert.NotContains(t, q, "__CLUSTER__")
		}
	}
	assert.True(t, sawBuilderQuery, "cpu_request must still come from the Prometheus builder")
}

func TestOoSubtractPairsByTimestamp(t *testing.T) {
	a := ooSeries{Timestamps: []int64{1, 2, 3}, Values: []float64{300, 300, 300}}
	// b is missing ts=2 -- a collector on a different cadence. Index-wise subtraction would
	// pair a[1] with b's ts=3 sample and report a wrong usage.
	b := ooSeries{Timestamps: []int64{1, 3}, Values: []float64{210, 190}}

	got := ooSubtract(a, b)
	assert.Equal(t, []int64{1, 3}, got.Timestamps)
	assert.Equal(t, []float64{90, 110}, got.Values)
}

func TestOoPercentileInterpolates(t *testing.T) {
	s := ooSeries{Values: []float64{10, 20, 30, 40}}
	assert.InDelta(t, 25, ooPercentile(0.5)(s), 0.001)
	assert.InDelta(t, 37, ooPercentile(0.9)(s), 0.001)
	assert.InDelta(t, 10, ooPercentile(0)(s), 0.001)
	assert.InDelta(t, 40, ooPercentile(1)(s), 0.001)
	assert.InDelta(t, 5, ooPercentile(0.5)(ooSeries{Values: []float64{5}}), 0.001)
}

func TestBuildOpenObserveClusterQueryShape(t *testing.T) {
	counter := buildOpenObserveClusterQuery(openObserveClusterCandidate{
		Family: "node_cpu_seconds_total", Counter: true, ExcludeIdle: true}, "300s")
	assert.Equal(t, `sum(rate(node_cpu_seconds_total{mode!="idle"}[300s]))`, counter)

	gauge := buildOpenObserveClusterQuery(openObserveClusterCandidate{Family: "machine_cpu_cores"}, "300s")
	assert.Equal(t, "sum(machine_cpu_cores{})", gauge)
}

// OpenObserve normalises each metric's stream name to lower case, so an instance fed
// node_memory_MemTotal_bytes answers only to node_memory_memtotal_bytes. Confirmed against
// a live instance: its __name__ values list carries node_memory_memtotal_bytes after
// node_memory_MemTotal_bytes was ingested. These are the only capitalised families in the
// utilisation set, so without this memory blanks while CPU resolves perfectly.
func TestOpenObserveClusterAggregatesDiscoverLowercasedMemoryFamilies(t *testing.T) {
	server, _ := openObserveTestBackend(t, map[string][]float64{
		"node_memory_memtotal_bytes":     {300, 300},
		"node_memory_memavailable_bytes": {210, 200},
	})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	got := resultsByKey(fetchClusterUtilisation(t, []string{"mem_total", "mem_real"}))
	require.Nil(t, got["mem_total"].Error)
	assert.InDelta(t, 300, got["mem_total"].Payload[0].Values[0], 0.01)
	require.Nil(t, got["mem_real"].Error)
	assert.InDelta(t, 100, got["mem_real"].Payload[0].Values[0], 0.01)
}

// The card fetches in three separate calls (cpu, memory, percentiles). Probing every role
// on each one would put three times the necessary query load on the customer's OpenObserve,
// so the batch is scoped to the roles the requested keys actually need.
func TestOpenObserveClusterAggregatesOnlyProbeNeededRoles(t *testing.T) {
	server, queries := openObserveTestBackend(t, map[string][]float64{
		"machine_cpu_cores": {84, 84},
	})
	defer server.Close()
	patches := patchOpenObserve(t, server.URL)
	defer patches.Reset()

	fetchClusterUtilisation(t, []string{"cpu_total"})

	for _, q := range queries() {
		assert.NotContains(t, q, "memory", "cpu_total must not probe memory families: %s", q)
		assert.NotContains(t, q, "node_cpu_seconds_total", "cpu_total must not probe usage families: %s", q)
	}
	assert.Len(t, queries(), len(OpenObserveClusterCandidates[ooRoleCPUCapacity]),
		"exactly the capacity candidates, nothing else")
}

// meta.Step is a PromQL duration string. promAggWindow emits seconds today, but parsing
// only the "<n>s" form would make a later switch to minutes fall through to the default
// and silently halve the resolution of every derived percentile.
func TestOpenObserveClusterStepParsesAnyDuration(t *testing.T) {
	req := GetUtilisationTrendRequest{}
	for _, tc := range []struct {
		step string
		want int
	}{
		{"60s", 60},
		{"1800s", 1800},
		{"5m", 300},
		{"1h", 3600},
		{"90", 90}, // bare integer, no unit
	} {
		assert.Equal(t, tc.want, openObserveClusterStep(RequestMetadata{Step: tc.step}, req), "step %q", tc.step)
	}

	// Unparseable or absent falls back to the request, then to a 60s floor.
	assert.Equal(t, 45, openObserveClusterStep(RequestMetadata{Step: "banana"}, GetUtilisationTrendRequest{StepInterval: 45}))
	assert.Equal(t, 60, openObserveClusterStep(RequestMetadata{}, req))
}

// The fallback is per-NAME within one provider, not across providers.
//
// cAdvisor publishes each network direction under its own metric name; the OTel receiver
// publishes both under k8s_pod_network_io and separates them with a `direction` label.
// Probing cAdvisor first means an instance already agreeing with Prometheus needs no
// translation, and one fed by OTel is translated without anyone configuring it.
func TestOpenObserveNetworkCandidates_FallBackFromCadvisorToOtel(t *testing.T) {
	rx := OpenObserveClusterCandidates[ooRoleNetRx]
	require.Len(t, rx, 2)

	assert.Equal(t, "container_network_receive_bytes_total", rx[0].Family)
	assert.Empty(t, rx[0].Matchers, "the cAdvisor name IS the direction")

	assert.Equal(t, "k8s_pod_network_io", rx[1].Family)
	assert.Equal(t, []string{`direction="receive"`}, rx[1].Matchers,
		"one OTel family carries both directions, so it must be narrowed")

	tx := OpenObserveClusterCandidates[ooRoleNetTx]
	assert.Equal(t, []string{`direction="transmit"`}, tx[1].Matchers)
	assert.NotEqual(t, rx[1].Matchers, tx[1].Matchers,
		"receive and transmit must not resolve to the same series")
}

// A bytes-per-bucket key is not a bytes-per-second key. Answering one with the other is
// wrong by exactly the bucket width, and both look plausible on a chart.
func TestBuildOpenObserveClusterQuery_IncreaseIsNotRate(t *testing.T) {
	inc := buildOpenObserveClusterQuery(OpenObserveClusterCandidates[ooRoleNetRx][1], "300s")
	assert.Contains(t, inc, "increase(")
	assert.NotContains(t, inc, "rate(")
	assert.Contains(t, inc, `direction="receive"`)

	// A plain counter still uses rate; only the per-bucket keys changed.
	rate := buildOpenObserveClusterQuery(OpenObserveClusterCandidates[ooRoleCPUUsage][0], "300s")
	assert.Contains(t, rate, "rate(")
	assert.NotContains(t, rate, "increase(")
	assert.Contains(t, rate, `mode!="idle"`, "ExcludeIdle must survive alongside Matchers")
}
