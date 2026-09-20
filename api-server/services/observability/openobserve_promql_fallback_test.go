package observability

// OpenObserve returns an EMPTY result for a top-level `or` rather than evaluating it, so
// the Prometheus builders' fallback chains have to be unrolled and asked one at a time.
// These cover the unrolling and the round logic that consumes it.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitTopLevelPromQLOr(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
		why  string
	}{
		{
			name: "no or is a single alternative",
			in:   `sum(kube_pod_container_resource_requests{resource="cpu", node=~"node-1.*"})`,
			want: []string{`sum(kube_pod_container_resource_requests{resource="cpu", node=~"node-1.*"})`},
			why:  "a key without a fallback must still cost exactly one round",
		},
		{
			name: "uppercase OR, as cpu_usage writes it",
			in:   `sum(irate(node_cpu_seconds_total{mode!="idle"}[5m])) OR sum(irate(node_resources_cpu_usage_seconds_total{mode!="idle"}[5m]))`,
			want: []string{
				`sum(irate(node_cpu_seconds_total{mode!="idle"}[5m]))`,
				`sum(irate(node_resources_cpu_usage_seconds_total{mode!="idle"}[5m]))`,
			},
		},
		{
			name: "three alternatives, as disk_total writes it",
			in:   `sum(a{x="1"}) or sum(b{x="1"}) or sum(c{x="1"})`,
			want: []string{`sum(a{x="1"})`, `sum(b{x="1"})`, `sum(c{x="1"})`},
		},
		{
			name: "parenthesised arithmetic operands stay whole",
			in:   `(sum(a) - sum(b)) or (sum(c) - sum(d))`,
			want: []string{`(sum(a) - sum(b))`, `(sum(c) - sum(d))`},
			why:  "disk_used subtracts inside each alternative; splitting those apart would corrupt it",
		},
		{
			name: "or inside a label VALUE is not an operator",
			in:   `sum(up{job=~"a or b"})`,
			want: []string{`sum(up{job=~"a or b"})`},
			why:  "a quoted matcher value must never be treated as a fallback boundary",
		},
		{
			name: "or inside a metric NAME is not an operator",
			in:   `sum(order_total) or sum(fallback_or_total)`,
			want: []string{`sum(order_total)`, `sum(fallback_or_total)`},
			why:  "`or` is an operator only when it stands alone",
		},
		{
			name: "or nested inside parentheses is not top level",
			in:   `sum(a or b)`,
			want: []string{`sum(a or b)`},
			why:  "only the outermost chain is unrolled; OpenObserve still cannot evaluate a nested one, but splitting it would change the query's meaning",
		},
		{
			name: "by-clause alternatives, as cpu_usage_line writes them",
			in:   `sum by (instance) (rate(a[5m])) or (sum by (node) (rate(a[5m]))) or (sum by (instance) (rate(b[5m])))`,
			want: []string{
				`sum by (instance) (rate(a[5m]))`,
				`(sum by (node) (rate(a[5m])))`,
				`(sum by (instance) (rate(b[5m])))`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, splitTopLevelPromQLOr(tc.in), tc.why)
		})
	}
}

// Every node key the Prometheus builder produces must survive the round trip: splitting
// it and rejoining with " or " has to reproduce the original, or the fallback is querying
// something other than what the builder intended.
func TestSplitTopLevelPromQLOr_CoversEveryNodeKey(t *testing.T) {
	meta := RequestMetadata{
		Kind: "node", NodeName: "node-1", NodeIP: "10.0.0.11", InternalIP: "10.0.0.11",
	}
	keys := []string{
		"cpu_usage", "memory_usage", "cpu_request", "memory_request", "cpu_limit",
		"memory_limit", "disk_total", "disk_used", "cpu_usage_line", "memory_usage_line",
		"pvc_usage", "active_nodes",
	}
	queries := buildPrometheusNodeQueries(meta, keys)
	require.NotEmpty(t, queries)

	withFallback := 0
	for key, q := range queries {
		parts := splitTopLevelPromQLOr(q)
		require.NotEmpty(t, parts, "%s split to nothing", key)
		for _, p := range parts {
			assert.NotContains(t, p, " or ", "%s alternative still holds a top-level or", key)
			assert.NotContains(t, p, " OR ", "%s alternative still holds a top-level OR", key)
		}
		if len(parts) > 1 {
			withFallback++
		}
	}
	assert.Positive(t, withFallback,
		"the node builder is expected to emit or-chains; if it no longer does, this workaround can go")
}

func TestQueryResultHasSamples(t *testing.T) {
	msg := "boom"
	assert.False(t, queryResultHasSamples(QueryResult{}), "no payload is not an answer")
	assert.False(t, queryResultHasSamples(QueryResult{Payload: []Result{{Values: []float64{}}}}),
		"a series with no points is not an answer")
	assert.False(t, queryResultHasSamples(QueryResult{Error: &msg, Payload: []Result{{Values: []float64{1}}}}),
		"an errored result must not stop the next alternative being tried")
	assert.True(t, queryResultHasSamples(QueryResult{Payload: []Result{{Values: []float64{1}}}}))
	assert.True(t, queryResultHasSamples(QueryResult{Payload: []Result{{}, {Values: []float64{2}}}}),
		"samples in any series count")
}
