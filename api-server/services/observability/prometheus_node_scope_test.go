package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nodeScopeQuery(t *testing.T, key string, meta RequestMetadata) string {
	t.Helper()
	meta.Kind = "node"
	q := buildPrometheusNodeQueries(meta, []string{key})[key]
	require.NotEmpty(t, q, "%s must be built", key)
	_, err := promQLParser.ParseExpr(stripClusterPlaceholder(q))
	require.NoError(t, err, "%s must be valid PromQL:\n%s", key, q)
	return q
}

// A node name with no IP used to match EVERY node: node-exporter identifies a machine
// by address, so the interpolated `instance=~"%s.*"` collapsed to `instance=~".*"`.
// Measured on dev before the fix: 42.96 GB reported for a node with 12.06 GB
// allocatable — silently, because a regex that matches everything is not an error.
func TestNodeScopeUsage_NameWithoutIPScopesToThatNode(t *testing.T) {
	for _, key := range []string{"memory_usage", "cpu_usage"} {
		q := nodeScopeQuery(t, key, RequestMetadata{NodeName: "node-1"})

		assert.NotContains(t, q, `instance=~".*"`, "%s must never match every node", key)
		assert.Contains(t, q, `on(pod, namespace) group_left(node) kube_pod_info{node="node-1"}`,
			"%s: node-exporter carries no node name, so it joins through its DaemonSet pod", key)
	}
}

// The IP is still the cheapest form and the nodes page already supplies it, so it must
// not grow a join it does not need.
func TestNodeScopeUsage_IPKeepsThePlainSelector(t *testing.T) {
	for _, key := range []string{"memory_usage", "cpu_usage"} {
		q := nodeScopeQuery(t, key, RequestMetadata{NodeName: "node-1", InternalIP: "10.0.0.5"})

		assert.Contains(t, q, `instance=~"10.0.0.5.*"`, key)
		assert.NotContains(t, q, "kube_pod_info", "%s: no join needed when the address is known", key)
	}
}

// No node at all must match nothing on BOTH arms. The node-agent arm was the second
// half of the same bug: it keyed on the node name, so an absent name matched every
// instance there too.
func TestNodeScopeUsage_NoScopeMatchesNothingOnEitherArm(t *testing.T) {
	// Every selector in each query, not just the first: memory subtracts available from
	// total on the node-agent arm, so it has three.
	for key, selectors := range map[string]int{"memory_usage": 3, "cpu_usage": 2} {
		q := nodeScopeQuery(t, key, RequestMetadata{})

		assert.NotContains(t, q, `=~".*"`, "%s: a caller with no node gets a gap, not the cluster", key)
		assert.Equal(t, selectors, countOccurrencesIn(q, promNoNodeScope),
			"%s: every selector is pinned shut, node-exporter and node-agent alike", key)
	}
}

// The rate window belongs to the selector, not to the joined expression — `irate(a * b)[5m]`
// is not valid PromQL, and the parser above is what catches it.
func TestNodeScopeCPU_RateAppliesToTheSeriesNotTheJoin(t *testing.T) {
	q := nodeScopeQuery(t, "cpu_usage", RequestMetadata{NodeName: "node-1"})
	assert.Contains(t, q, `irate(node_cpu_seconds_total{mode!="idle"}[5m]) * on(pod, namespace)`)
}

func countOccurrencesIn(s, sub string) int {
	n, i := 0, 0
	for {
		j := indexFrom(s, sub, i)
		if j < 0 {
			return n
		}
		n++
		i = j + len(sub)
	}
}

func indexFrom(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	if i := indexOf(s[from:], sub); i >= 0 {
		return from + i
	}
	return -1
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
