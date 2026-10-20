package observability

import (
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httpScopeKeys is every utilisation key whose selector comes from
// httpWorkloadScope -- the node-agent's eBPF HTTP metrics.
var httpScopeKeys = []string{
	"http_status",
	"http_max_response_time",
	"workload_http_latency_p99",
	"workload_http_request_rate",
	"workload_http_latency_mean",
}

// httpScopeMatchers returns every matcher the key's query applies to `label`.
//
// The AST is walked rather than the query string searched, because these queries nest:
// a histogram_quantile over a sum, and a division carrying a selector on each side. A
// scope that is correct in the numerator and wrong in the denominator is exactly what a
// strings.Contains assertion cannot see.
func httpScopeMatchers(t *testing.T, meta RequestMetadata, key, label string) []*labels.Matcher {
	t.Helper()
	q := buildPrometheusWorkloadQueries(meta, []string{key})[key]
	require.NotEmpty(t, q, "%s must still be built", key)

	expr, err := promQLParser.ParseExpr(stripClusterPlaceholder(q))
	require.NoError(t, err, "%s must be valid PromQL:\n%s", key, q)

	var out []*labels.Matcher
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if vs, ok := node.(*parser.VectorSelector); ok {
			for _, m := range vs.LabelMatchers {
				if m.Name == label {
					out = append(out, m)
				}
			}
		}
		return nil
	})
	return out
}

// A workload's traffic is its own. These keys used to select it by pod-name prefix over
// actual_destination_workload_name, which holds a POD name, so `web.*` also matched
// every pod of `web-worker` and the answer was the two workloads added together.
//
// The assertion is made by asking the real matcher, not by comparing strings: the point
// is what the selector MATCHES, and Prometheus anchoring is the thing being relied on.
func TestHTTPScopeKeys_SelectOneWorkloadNotItsPrefixSiblings(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "web",
		RangeWindow: "14d", Step: "1h", RateWindow: "5m",
	}
	for _, key := range httpScopeKeys {
		ms := httpScopeMatchers(t, meta, key, "destination_workload_name")
		require.NotEmpty(t, ms, "%s must scope by the workload-identity label", key)
		for _, m := range ms {
			assert.True(t, m.Matches("web"), "%s must match the workload asked for", key)
			assert.False(t, m.Matches("web-worker"), "%s must NOT match a workload whose name merely starts with it", key)
			assert.False(t, m.Matches("web-2"), "%s must not match a differently-suffixed sibling", key)
		}
		// The pod-name label must not be used to identify a workload at all.
		assert.Empty(t, httpScopeMatchers(t, meta, key, "actual_destination_workload_name"),
			"%s must not select a workload through the pod-name label", key)
	}
}

// Every matcher on the pod-name label must be exact. A prefix there is the defect, and
// it regressed once by being inlined per key rather than built in one place.
func TestHTTPScopeKeys_NoPrefixMatcherSurvives(t *testing.T) {
	for _, meta := range []RequestMetadata{
		{Kind: "deployment", Namespace: "shop", Name: "web", RangeWindow: "14d", Step: "1h", RateWindow: "5m"},
		{Kind: "statefulset", Namespace: "shop", Name: "web", RangeWindow: "14d", Step: "1h", RateWindow: "5m"},
		{Kind: "pod", Namespace: "shop", Name: "web-5849b5c898-hmczg", RangeWindow: "14d", Step: "1h", RateWindow: "5m"},
	} {
		for _, key := range httpScopeKeys {
			for _, label := range []string{"destination_workload_name", "actual_destination_workload_name"} {
				for _, m := range httpScopeMatchers(t, meta, key, label) {
					assert.Equal(t, labels.MatchEqual, m.Type,
						"%s/%s on %s must be an exact match, not a regex", meta.Kind, key, label)
				}
			}
		}
	}
}

// A pod-kind request is the one case that must stay on the pod-name label: its Name IS
// a pod name, which only actual_destination_workload_name carries. The pod details
// page's "Request & Latency" chart asks for http_status this way, and scoping it by the
// controller label would return nothing at all rather than a narrower answer.
func TestHTTPScopeKeys_PodKindStaysOnThePodLabel(t *testing.T) {
	meta := RequestMetadata{
		Kind: "pod", Namespace: "shop", Name: "web-5849b5c898-hmczg",
		RangeWindow: "14d", Step: "1h", RateWindow: "5m",
	}
	for _, key := range httpScopeKeys {
		ms := httpScopeMatchers(t, meta, key, "actual_destination_workload_name")
		require.NotEmpty(t, ms, "%s must scope a pod by the pod-name label", key)
		for _, m := range ms {
			assert.True(t, m.Matches("web-5849b5c898-hmczg"), "%s must match the pod asked for", key)
			assert.False(t, m.Matches("web-5849b5c898-other"), "%s must not match a sibling pod", key)
		}
		assert.Empty(t, httpScopeMatchers(t, meta, key, "destination_workload_name"),
			"%s must not ask the controller label for a pod name -- it never carries one", key)
	}
}

// A namespace-scoped request stays namespace-wide. The keys reached this path before
// with a no-op `actual_destination_workload_name=~".*"`; dropping that matcher must not
// narrow the answer, and must not start scoping by destination_workload_namespace,
// which selects a different set of series for node-destined traffic.
func TestHTTPScopeKeys_NamespaceOnlyStaysNamespaceWide(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop",
		RangeWindow: "14d", Step: "1h", RateWindow: "5m",
	}
	for _, key := range httpScopeKeys {
		assert.Empty(t, httpScopeMatchers(t, meta, key, "destination_workload_name"),
			"%s must not invent a workload when none was asked for", key)
		assert.Empty(t, httpScopeMatchers(t, meta, key, "actual_destination_workload_name"),
			"%s must not invent a workload when none was asked for", key)

		ns := httpScopeMatchers(t, meta, key, "actual_destination_workload_namespace")
		require.NotEmpty(t, ns, "%s must still be bounded by the namespace", key)
		for _, m := range ns {
			assert.True(t, m.Matches("shop"))
			assert.False(t, m.Matches("other"))
		}
		assert.Empty(t, httpScopeMatchers(t, meta, key, "destination_workload_namespace"),
			"%s must keep reading the namespace the pod served in, not the one addressed", key)
	}
}

// The scope is one seam, so the two filter variables and the three inlined keys cannot
// drift apart again. http_status reaches it through actualDestFilter and
// workload_http_request_rate inlines it; both must render the same scope.
func TestHTTPScopeKeys_ShareOneScope(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "web",
		RangeWindow: "14d", Step: "1h", RateWindow: "5m",
	}
	want := promWorkloadHTTPMatcher("deployment", "web")
	for _, key := range httpScopeKeys {
		q := buildPrometheusWorkloadQueries(meta, []string{key})[key]
		assert.Contains(t, q, want, "%s must scope through the shared matcher", key)
		assert.Contains(t, q, `actual_destination_workload_namespace="shop"`, "%s keeps the namespace it always had", key)
	}
}

// The sibling keys that were already scoped correctly must not be dragged onto the new
// matcher: they answer per path and method off destination_workload_*, and
// workload_http_error_rate is a ratio the fix deliberately did not touch.
func TestHTTPScopeKeys_LeaveTheAlreadyCorrectKeysAlone(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "web",
		RangeWindow: "14d", Step: "1h", RateWindow: "5m",
	}
	for _, key := range []string{"http_throughput", "workload_http_error_rate", "http_latency_p95"} {
		ms := httpScopeMatchers(t, meta, key, "destination_workload_name")
		require.NotEmpty(t, ms, "%s was already scoped by the workload label", key)
		for _, m := range ms {
			assert.True(t, m.Matches("web"))
			assert.False(t, m.Matches("web-worker"), "%s was already exact and must stay so", key)
		}
	}
}
