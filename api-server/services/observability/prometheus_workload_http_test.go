package observability

import (
	"strings"
	"testing"

	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workloadHTTP(t *testing.T, key string) string {
	t.Helper()
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "api",
		RangeWindow: "14d", Step: "1h", RateWindow: "5m",
	}
	q := buildPrometheusWorkloadQueries(meta, []string{key})[key]
	require.NotEmpty(t, q, "%s must be built", key)
	_, err := promQLParser.ParseExpr(stripClusterPlaceholder(q))
	require.NoError(t, err, "%s must be valid PromQL:\n%s", key, q)
	return q
}

// Probe traffic is not request traffic. On the dev cluster a large share of workloads
// see nothing else, so a rate that counts it reports the scrape interval rather than
// the workload's load.
func TestWorkloadHTTPKeys_ExcludeProbeTraffic(t *testing.T) {
	for _, key := range []string{"workload_http_request_rate", "workload_http_latency_mean"} {
		q := workloadHTTP(t, key)
		assert.Contains(t, q, `path!~`, "%s must exclude probe paths", key)
		for _, probe := range []string{"/health", "/healthz", "/livez", "/actuator/health"} {
			assert.Contains(t, q, probe, "%s should exclude %s", key, probe)
		}
	}
}

// The exclusion must be one definition, not one per caller: the same key on two
// accounts has to mean the same thing.
func TestHealthCheckPathsRegex_IsStableAndEscaped(t *testing.T) {
	re := healthCheckPathsRegex()
	assert.Equal(t, re, healthCheckPathsRegex(), "the list is ordered, so the query text is stable")
	assert.Contains(t, re, `/actuator/health`)
	assert.NotContains(t, re, " ", "a space would break the PromQL selector")

	// A path carrying a regex metacharacter must not become one.
	assert.Equal(t, `/v1\.0/health`, escapePromQLRegex("/v1.0/health"))
	assert.Equal(t, `/a\+b`, escapePromQLRegex("/a+b"))
}

// `/health` must not swallow `/healthy-users`. PromQL anchors a label regex at both
// ends, so alternation alone is enough — this pins that assumption by asking the real
// matcher, not by reasoning about it.
func TestHealthCheckPaths_DoNotPrefixMatchRealEndpoints(t *testing.T) {
	expr := `up{path!~"` + healthCheckPathsRegex() + `"}`
	parsed, err := promQLParser.ParseExpr(expr)
	require.NoError(t, err)

	vs, ok := parsed.(*parser.VectorSelector)
	require.True(t, ok)

	checked := 0
	for _, m := range vs.LabelMatchers {
		if m.Name != "path" {
			continue
		}
		checked++
		assert.True(t, m.Matches("/healthy-users"), "a real endpoint whose name starts with a probe's is KEPT")
		assert.True(t, m.Matches("/api/v1/orders"), "ordinary traffic is kept")
		assert.False(t, m.Matches("/health"), "a probe path is excluded")
		assert.False(t, m.Matches("/actuator/health"), "so is a nested one")
	}
	require.Equal(t, 1, checked, "the path matcher must be present")
}

// The mean is sum/count, and an idle window must be absent rather than +Inf.
func TestWorkloadHTTPLatencyMean_GuardsTheDenominator(t *testing.T) {
	q := workloadHTTP(t, "workload_http_latency_mean")
	assert.Contains(t, q, "_sum{")
	assert.Contains(t, q, "_count{")
	assert.Contains(t, q, "> 0", "an idle window divides by zero without this")
	assert.Equal(t, 1, strings.Count(q, " / "), "one division, not a nested one")
}

// These are workload totals. The per-path http_throughput / http_latency_sum keys
// answer a different question and must keep answering it.
func TestWorkloadHTTPKeys_AreNotThePerPathOnes(t *testing.T) {
	rate := workloadHTTP(t, "workload_http_request_rate")
	assert.NotContains(t, rate, "by(method", "a workload total has no path or method dimension")
	assert.NotContains(t, rate, "by (method")

	meta := RequestMetadata{Kind: "deployment", Namespace: "shop", Name: "api", RangeWindow: "14d", Step: "1h", RateWindow: "5m"}
	perPath := buildPrometheusWorkloadQueries(meta, []string{"http_throughput"})["http_throughput"]
	assert.Contains(t, perPath, "method", "http_throughput still breaks down by path and method")
}

// Datadog answers the same concepts from APM, which is the whole point of naming the
// metric rather than the query.
func TestDatadogWorkloadHTTPKeys_ComeFromAPM(t *testing.T) {
	meta := RequestMetadata{Kind: "deployment", Namespace: "shop", Name: "api", RangeWindow: "14d"}
	q := buildDatadogWorkloadQueries(meta, []string{"workload_http_request_rate", "workload_http_latency_mean"})

	assert.Contains(t, q["workload_http_request_rate"], "trace.http.server.request.hits")
	assert.Contains(t, q["workload_http_request_rate"], ".as_rate()")
	assert.Contains(t, q["workload_http_latency_mean"], "avg:trace.http.server.request")
	for _, v := range q {
		assert.NotContains(t, v, "path!~", "APM is not traced for probes, so no path filter is needed")
	}
}
