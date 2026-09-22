package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every generated query, in every request shape, must be valid PromQL.
//
// Golden-string tests pin the text but cannot tell whether the text is a legal
// query: a missing or doubled comma renders fine, reads fine, and is rejected by
// Prometheus at request time. Two such bugs reached review in this change alone —
// `,,` when the filter ended in a comma, and a leading `,` when the filter was empty
// because the request carried no namespace — so the shapes are enumerated here and
// handed to Prometheus's own parser.
func TestPerPodQueriesAreValidPromQL(t *testing.T) {
	keys := []string{
		"container_cpu_usage_by_pod",
		"container_memory_working_set_by_pod",
		"container_memory_max_usage_by_pod",
		// The join is the one query here the parser earns its keep on: two selectors,
		// a vector match with an explicit `on`, and a group_left, all built from the
		// same shared filter string.
		"container_oomkilled_limit_by_pod",
	}

	shapes := map[string]RequestMetadata{
		// Cluster-wide: no namespace at all, so the shared filter is empty. This is
		// the shape the same builder already serves for p90_cpu / cpu_real.
		"no scope at all":      {Kind: "deployment"},
		"namespace only":       {Kind: "deployment", Namespace: "shop"},
		"workload":             {Kind: "deployment", Namespace: "shop", Name: "api"},
		"workload + container": {Kind: "deployment", Namespace: "shop", Name: "api", ContainerName: "api"},
		"single pod":           {Kind: "pod", Namespace: "shop", Name: "api-7d9f"},
		"exact pod list":       {Kind: "deployment", Namespace: "shop", Name: "api", PodNames: []string{"api-7d9f", "api-3b2c"}, PodNamesSupplied: true},
		"resolved to no pods":  {Kind: "deployment", Namespace: "shop", Name: "api", PodNames: []string{}, PodNamesSupplied: true},
	}

	for _, aggregate := range []string{"", "p92", "max", "count"} {
		parsed, err := parseAggregate(aggregate)
		require.NoError(t, err)

		for shapeName, meta := range shapes {
			meta := meta
			meta.Aggregate = parsed
			meta.RangeWindow, meta.Step, meta.RateWindow = "14d", "75s", "75s"

			queries := buildPrometheusWorkloadQueries(meta, keys)
			for _, key := range keys {
				query, built := queries[key]
				require.Truef(t, built, "%s/%s/%s produced no query", shapeName, key, aggregate)

				// The placeholder is substituted per account before execution; the
				// direct-API path strips it, which is the harsher of the two.
				// promQLParser is the package's own shared parser, the same one
				// scopePromQLSelectors rewrites queries with.
				_, parseErr := promQLParser.ParseExpr(stripClusterPlaceholder(query))
				require.NoErrorf(t, parseErr, "aggregate=%q shape=%q key=%q produced invalid PromQL:\n%s",
					aggregate, shapeName, key, query)
			}
		}
	}
}

// A pod name carrying a regex metacharacter must still render a parseable query.
// RFC1123 allows "." in a pod name, and the wrong escape order emits pod=~"my\.pod",
// which the parser rejects with `unknown escape sequence` — failing the whole query,
// not just that pod.
func TestPromPodNameAlternation_EscapesForBothRegexAndStringLiteral(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "nudgebee", Name: "services-server",
		RangeWindow: "1209600s", Step: "75s", RateWindow: "300s",
		PodNames:         []string{"my.pod-abc", `quote"pod`, `back\slash`},
		PodNamesSupplied: true,
	}
	queries := buildPrometheusWorkloadQueries(meta, []string{"container_cpu_usage_by_pod"})
	query := queries["container_cpu_usage_by_pod"]

	assert.Contains(t, query, `my\\.pod-abc`, "the dot is a literal dot, doubly escaped")
	_, err := promQLParser.ParseExpr(stripClusterPlaceholder(query))
	require.NoError(t, err, "query must parse: %s", query)
}

// An exact pod list is scope on its own. Without a workload name the matcher used to
// be dropped entirely, widening the query to the whole namespace.
func TestPodNamesScopeWithoutAWorkloadName(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "nudgebee",
		RangeWindow: "1209600s", Step: "75s", RateWindow: "300s",
		PodNames:         []string{"services-server-7d9f"},
		PodNamesSupplied: true,
	}
	queries := buildPrometheusWorkloadQueries(meta, []string{"container_cpu_usage_by_pod"})
	query := queries["container_cpu_usage_by_pod"]

	assert.Contains(t, query, `pod=~"services-server-7d9f"`)
	_, err := promQLParser.ParseExpr(stripClusterPlaceholder(query))
	require.NoError(t, err)

	// And the empty list still matches nothing rather than the namespace.
	meta.PodNames = []string{}
	empty := buildPrometheusWorkloadQueries(meta, []string{"container_cpu_usage_by_pod"})["container_cpu_usage_by_pod"]
	assert.Contains(t, empty, `pod="__no_pods_resolved__"`)
}
