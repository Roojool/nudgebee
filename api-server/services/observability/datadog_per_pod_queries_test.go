package observability

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func datadogPerPodMeta(t *testing.T, aggregate string, pods int) RequestMetadata {
	t.Helper()
	parsed, err := parseAggregate(aggregate)
	require.NoError(t, err)
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "nudgebee", Name: "services-server",
		ContainerName: "services-server", RangeWindow: "1209600s", // 14 days
		Aggregate: parsed,
	}
	for i := 0; i < pods; i++ {
		meta.PodNames = append(meta.PodNames, fmt.Sprintf("services-server-7d9f-%02d", i))
	}
	meta.PodNamesSupplied = pods > 0
	return meta
}

// A plain aggregate is pushed down: one bucket as wide as the window IS the collapse,
// and it costs one value per pod on the wire instead of a series.
func TestBuildDatadogPerPodQuery_PlainAggregateRollsUpTheWholeWindow(t *testing.T) {
	meta := datadogPerPodMeta(t, "max", 6)
	meta.PodNames, meta.PodNamesSupplied = nil, false
	got := buildDatadogPerPodQuery(datadogPerPodMetricMemory, meta, "kube_namespace:nudgebee", 1209600)
	assert.Equal(t,
		"avg:kubernetes.memory.working_set{kube_namespace:nudgebee} by {pod_name,kube_container_name}.rollup(max, 1209600)",
		got)
}

// A percentile cannot be pushed down — Datadog has no percentile over time for a
// gauge — so the query asks for the finest resolution the budget allows and the
// arithmetic happens in Go.
func TestBuildDatadogPerPodQuery_PercentileAsksForResolution(t *testing.T) {
	meta := datadogPerPodMeta(t, "p99", 6)
	got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta, "kube_namespace:nudgebee", 1209600)
	assert.Contains(t, got, ".rollup(avg, 300)", "a six-pod workload can afford 5-minute buckets")
	assert.NotContains(t, got, "p99", "Datadog has no percentile for this; it is computed in Go")
}

// Left alone, Datadog rolls a 14-day query into 2-hour buckets — 168 points per pod,
// where a "p99" is barely more than a max. Explicit resolution is the point; the
// budget is what stops it costing tens of megabytes on a churny workload.
func TestDatadogRollupSecondsForPods_TradesResolutionForSize(t *testing.T) {
	const fourteenDays = int64(14 * 24 * 3600)

	assert.Equal(t, 300, datadogRollupSecondsForPods(fourteenDays, 6),
		"a typical workload gets 5-minute buckets: ~24k points, well under a megabyte")
	assert.Equal(t, 300, datadogRollupSecondsForPods(fourteenDays, 49))

	// The dev workload that produced the 28.5 MB measurement: 387 pod incarnations.
	coarser := datadogRollupSecondsForPods(fourteenDays, 387)
	assert.Greater(t, coarser, 300, "churn costs resolution rather than megabytes")
	points := (fourteenDays / int64(coarser)) * 387
	assert.LessOrEqual(t, points, int64(datadogPerPodPointBudget))

	assert.Equal(t, 300, datadogRollupSecondsForPods(fourteenDays, 0), "no pod list is not zero pods")
}

// The key Datadog cannot answer builds no query and comes back carrying the reason.
// Silence would be indistinguishable from a workload that allocated nothing, and
// rightsizing acts on that difference.
func TestDatadogHighWaterMark_ComesBackWithAReasonNotSilence(t *testing.T) {
	meta := datadogPerPodMeta(t, "max", 6)
	requested := []string{"container_memory_max_usage_by_pod", "container_cpu_usage_by_pod"}

	queries := buildDatadogWorkloadQueries(meta, requested)
	assert.NotContains(t, queries, "container_memory_max_usage_by_pod", "no query is sent for it")
	assert.Contains(t, queries, "container_cpu_usage_by_pod")

	// What the caller actually receives.
	results := datadogNoteUnanswerablePerPodKeys(
		[]QueryResult{{QueryKey: "container_cpu_usage_by_pod", Payload: []Result{{}}}}, requested)

	require.Len(t, results, 2)
	assert.Equal(t, "container_memory_max_usage_by_pod", results[1].QueryKey)
	assert.Empty(t, results[1].Payload)
	assert.Contains(t, results[1].Note, "no cgroup high-water mark")
	assert.Empty(t, results[0].Note, "a key Datadog answered says nothing extra")
}

// The collapse reduces each pod's OWN values over time — not across pods at one
// timestamp, which is a different question that shares the name "p90".
func TestDatadogCollapseOverTime(t *testing.T) {
	p99, err := parseAggregate("p99")
	require.NoError(t, err)

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload: []Result{
			{Metric: map[string]string{"pod_name": "a"}, Timestamps: []int64{1, 2, 3}, Values: []float64{1, 2, 9}},
			{Metric: map[string]string{"pod_name": "b"}, Timestamps: []int64{1, 2, 3}, Values: []float64{5, 5, 5}},
		},
	}}
	datadogCollapseOverTime(results, p99)

	require.Len(t, results[0].Payload[0].Values, 1)
	assert.InDelta(t, 8.86, results[0].Payload[0].Values[0], 0.01, "pod a's own p99, near its peak of 9")
	assert.InDelta(t, 5, results[0].Payload[1].Values[0], 0.01, "pod b never moved")
	assert.Equal(t, []int64{3}, results[0].Payload[0].Timestamps, "stamped with its last observation")
}

// A gap is not a measurement. Counting NaN as zero is what made every Datadog
// percentile read low before nulls were carried as gaps.
func TestPercentileOverTime_IgnoresGaps(t *testing.T) {
	withGaps := []float64{math.NaN(), 10, math.NaN(), 20}
	got, ok := percentileOverTime(withGaps, 0.5)
	require.True(t, ok)
	assert.InDelta(t, 15, got, 0.001, "median of 10 and 20, not of 0, 0, 10, 20")

	_, ok = percentileOverTime([]float64{math.NaN(), math.NaN()}, 0.99)
	assert.False(t, ok, "nothing readable is not a value of zero")

	single, ok := percentileOverTime([]float64{7}, 0.99)
	require.True(t, ok)
	assert.Equal(t, float64(7), single)
}

// Only the per-pod keys collapse; a chart key asked for in the same request keeps its
// series, and a plain aggregate is already collapsed by the rollup.
func TestDatadogCollapseOverTime_LeavesOtherKeysAlone(t *testing.T) {
	p99, err := parseAggregate("p99")
	require.NoError(t, err)
	max, err := parseAggregate("max")
	require.NoError(t, err)

	series := func() []QueryResult {
		return []QueryResult{{QueryKey: "cpu_usage", Payload: []Result{
			{Timestamps: []int64{1, 2, 3}, Values: []float64{1, 2, 3}},
		}}}
	}

	other := series()
	datadogCollapseOverTime(other, p99)
	assert.Len(t, other[0].Payload[0].Values, 3, "cpu_usage is not a per-pod key")

	perPod := []QueryResult{{QueryKey: "container_cpu_usage_by_pod", Payload: []Result{
		{Timestamps: []int64{1, 2, 3}, Values: []float64{1, 2, 3}},
	}}}
	datadogCollapseOverTime(perPod, max)
	assert.Len(t, perPod[0].Payload[0].Values, 3, "a plain aggregate was already collapsed by the rollup")
}

// The pod list must reach the query, or Datadog answers with every pod under the
// workload tag while the rollup was budgeted for the few that were asked for.
//
// Datadog rejects a scope mixing comma-separated filters with boolean ones, so naming
// pods re-joins the whole filter with AND. Both forms were verified against the live
// API before being pinned here.
func TestDatadogPerPodFilter_ScopesToTheExactPodList(t *testing.T) {
	meta := datadogPerPodMeta(t, "p99", 2)
	got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta,
		"kube_namespace:nudgebee, kube_deployment:services-server", 1209600)

	assert.Contains(t, got,
		"{kube_namespace:nudgebee AND kube_deployment:services-server AND "+
			"(pod_name:services-server-7d9f-00 OR pod_name:services-server-7d9f-01)}")
	assert.NotContains(t, got, "nudgebee,", "a boolean scope may not also use commas")
}

// A caller that resolved the pods and found none gets nothing back. Falling back to
// the workload tag would size the workload on pods the caller had already ruled out —
// the same failure the Prometheus builder guards with the same sentinel.
func TestDatadogPerPodFilter_EmptyListMatchesNothing(t *testing.T) {
	meta := datadogPerPodMeta(t, "max", 0)
	meta.PodNamesSupplied = true

	got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta, "kube_namespace:nudgebee", 1209600)
	assert.Contains(t, got, "pod_name:__no_pods_resolved__")
	assert.Equal(t, `pod="__no_pods_resolved__"`,
		`pod="`+datadogNoPodsSentinel+`"`, "both providers spell 'no pods' the same way")
}

// A single pod needs no OR group, and an unscoped request keeps today's comma filter
// byte-for-byte.
func TestDatadogPerPodFilter_SinglePodAndUnscoped(t *testing.T) {
	single := datadogPerPodMeta(t, "max", 1)
	filter := datadogPerPodFilter("kube_namespace:nudgebee", single)
	assert.Equal(t, "kube_namespace:nudgebee AND pod_name:services-server-7d9f-00", filter,
		"no OR group is needed for one pod")

	unscoped := datadogPerPodMeta(t, "max", 0)
	got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, unscoped,
		"kube_namespace:nudgebee, kube_deployment:services-server", 1209600)
	assert.Contains(t, got, "{kube_namespace:nudgebee, kube_deployment:services-server}")
	assert.NotContains(t, got, " AND ")
}

// Without a pod list there is no series count, so there is nothing to size the budget
// from. Guessing the finest rollup is how a churny workload returns tens of megabytes.
func TestBuildDatadogPerPodQuery_UnknownPodCountTakesTheCoarsestRollup(t *testing.T) {
	meta := datadogPerPodMeta(t, "p99", 0)
	got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta, "kube_namespace:nudgebee", 1209600)
	assert.Contains(t, got, ".rollup(avg, 3600)")

	scoped := datadogPerPodMeta(t, "p99", 6)
	got = buildDatadogPerPodQuery(datadogPerPodMetricCPU, scoped, "kube_namespace:nudgebee", 1209600)
	assert.Contains(t, got, ".rollup(avg, 300)", "a known, small pod list buys resolution")
}

// Aggregate promises exactly one point per series. A pod that reported nothing all
// window is still one point — null, not a zero that reads as a measured idle pod.
func TestDatadogCollapseOverTime_AllGapSeriesStillCollapses(t *testing.T) {
	p99, err := parseAggregate("p99")
	require.NoError(t, err)

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload: []Result{
			{Timestamps: []int64{1, 2, 3}, Values: []float64{math.NaN(), math.NaN(), math.NaN()}},
			{Timestamps: nil, Values: nil},
		},
	}}
	datadogCollapseOverTime(results, p99)

	require.Len(t, results[0].Payload[0].Values, 1, "one point, as Aggregate promises")
	assert.True(t, math.IsNaN(results[0].Payload[0].Values[0]), "a gap stays a gap")
	assert.Equal(t, []int64{3}, results[0].Payload[0].Timestamps)
	assert.Empty(t, results[0].Payload[1].Timestamps, "a series with no points has nothing to stamp")
}

// Guards the shape the live API accepts: no stray comma once the scope goes boolean.
func TestDatadogPerPodFilter_NeverMixesCommasWithBooleans(t *testing.T) {
	meta := datadogPerPodMeta(t, "p99", 3)
	filter := datadogPerPodFilter("kube_namespace:nudgebee, kube_deployment:services-server, kube_container_name:app", meta)

	require.Contains(t, filter, " AND ")
	assert.False(t, strings.Contains(filter, ","), "Datadog rejects a scope that mixes the two: %s", filter)
}

// The scope must stay boolean-only however the filter was assembled: a clause added
// without the space after the comma, or an empty filter, must not leave a stray comma
// or a leading " AND ". A malformed Datadog query fails every key in the request, not
// just this one.
func TestDatadogPerPodFilter_ToleratesHowTheFilterWasAssembled(t *testing.T) {
	meta := datadogPerPodMeta(t, "p95", 1)

	assert.Equal(t,
		"kube_namespace:nudgebee AND kube_deployment:web AND pod_name:services-server-7d9f-00",
		datadogPerPodFilter("kube_namespace:nudgebee,kube_deployment:web", meta),
		"a comma with no space still splits")

	assert.Equal(t, "pod_name:services-server-7d9f-00",
		datadogPerPodFilter("", meta), "an empty filter contributes no leading AND")

	assert.Equal(t, "kube_namespace:nudgebee AND pod_name:services-server-7d9f-00",
		datadogPerPodFilter("kube_namespace:nudgebee, ", meta), "a trailing comma is dropped")
}
