package observability

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The node totals come from the containers on the node, never from a node-level gauge.
//
// `memory_usage` / `cpu_usage` at node scope filter on `instance=~"<internal IP>.*"`,
// and this enricher only knows the node NAME. With no IP that regex matches every
// instance, so the card reported the whole cluster against one node's allocatable —
// measured on dev: 51.3 GB used of 12.9 GB allocatable, and 20.0 cores of 3.92.
func TestNoisyNeighbours_NodeTotalsAreTheSumOfItsContainers(t *testing.T) {
	results := map[string][]latestSample{
		"top_pods": {
			{Metric: map[string]string{"namespace": "a", "pod": "p1", "container": "c"}, Value: 100},
			{Metric: map[string]string{"namespace": "a", "pod": "p2", "container": "c"}, Value: 250},
		},
		"top_pods_cpu": {
			{Metric: map[string]string{"namespace": "a", "pod": "p1", "container": "c"}, Value: 0.25},
			{Metric: map[string]string{"namespace": "a", "pod": "p2", "container": "c"}, Value: 0.75},
		},
		"node_alloc":     {{Value: 1000}},
		"node_cpu_alloc": {{Value: 4}},
	}

	data, err := buildNoisyNeighbourData("node-1", results, true)
	require.NoError(t, err)

	assert.InDelta(t, 350, data.NodeUsed, 1e-9, "100 + 250, the containers the card lists")
	assert.InDelta(t, 1.0, data.NodeCPUUsed, 1e-9, "0.25 + 0.75")

}

// The total counts every container on the node, not the fifteen the card lists.
//
// Trimming it to the visible rows would under-report any node running more than
// fifteen containers, which is most of them: measured on dev, a node's fifteen rows
// summed to 3.48 GB against a real total of 4.92 GB. So the rows deliberately do NOT
// add up to the total, and this pins that rather than the reverse.
func TestNoisyNeighbours_NodeTotalCountsContainersTheCardDoesNotList(t *testing.T) {
	results := map[string][]latestSample{"node_alloc": {{Value: 1e12}}}
	for i := 0; i < noisyNeighboursTopN+5; i++ {
		results["top_pods"] = append(results["top_pods"], latestSample{
			Metric: map[string]string{"namespace": "a", "pod": fmt.Sprintf("p%d", i), "container": "c"},
			Value:  100,
		})
	}

	data, err := buildNoisyNeighbourData("node-1", results, false)
	require.NoError(t, err)

	assert.Len(t, data.Neighbours, noisyNeighboursTopN, "the card lists the top fifteen")
	assert.InDelta(t, float64(noisyNeighboursTopN+5)*100, data.NodeUsed, 1e-9,
		"the total counts all twenty")

	rowSum := 0.0
	for _, n := range data.Neighbours {
		rowSum += n["memory_used"].(float64)
	}
	assert.Less(t, rowSum, data.NodeUsed, "the visible rows are a subset, and say so")
}

// A node total must never exceed its allocatable just because a different node's
// usage leaked in. This is the shape of the bug, pinned.
func TestNoisyNeighbours_NodeTotalStaysWithinItsOwnNode(t *testing.T) {
	results := map[string][]latestSample{
		"top_pods":   {{Metric: map[string]string{"namespace": "a", "pod": "p1", "container": "c"}, Value: 8e9}},
		"node_alloc": {{Value: 12.9e9}},
	}
	data, err := buildNoisyNeighbourData("node-1", results, false)
	require.NoError(t, err)
	assert.Less(t, data.NodeUsed, data.NodeAllocatable,
		"one node's containers cannot outweigh its own allocatable")
}
