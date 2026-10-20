package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cluster utilisation tiles name no namespace and no workload, which is exactly
// the shape buildDatadogWorkloadQueries used to refuse: its `if filterStr == ""` guard
// returned before the block labelled "Node/Cluster Aggregations", whose queries are
// scoped {*} and need no filter at all. Measured live against a Datadog account, every
// tile came back empty — and empty with no note, which a caller cannot tell apart from
// a cluster that used nothing.
func TestDatadogClusterScope_AnswersTheClusterTiles(t *testing.T) {
	keys := []string{"cpu_usage_trend", "cpu_total", "mem_usage_trend", "mem_total"}

	cluster := buildDatadogWorkloadQueries(RequestMetadata{}, keys)
	for _, key := range keys {
		require.Contains(t, cluster, key, "%s must be built with no workload filter", key)
		assert.Contains(t, cluster[key], "{*}", "%s is cluster-wide, so it is scoped {*}", key)
	}

	// A workload-scoped request still gets them, unchanged: they describe the cluster
	// either way, and the tiles are the only caller that omits the filter.
	workload := buildDatadogWorkloadQueries(
		RequestMetadata{Kind: "deployment", Namespace: "shop", Name: "web"}, keys)
	for _, key := range keys {
		assert.Equal(t, cluster[key], workload[key], "%s must not depend on the scope", key)
	}
}
