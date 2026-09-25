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

// No aggregate is pushed down as a window-wide bucket any more.
//
// `.rollup(<agg>, <window>)` looks like the cheapest possible collapse — one value per
// pod on the wire — and Datadog does not answer it reliably. Measured live on a
// workload with 69 pod incarnations over 14 days, the window-wide form returned 28 of
// them; the loss starts around 5 days and is not monotonic, which is bucket alignment
// rather than volume. So the query asks for a bounded rollup and the collapse is
// arithmetic on this side.
func TestBuildDatadogPerPodQuery_PlainAggregateAsksForABoundedRollup(t *testing.T) {
	meta := datadogPerPodMeta(t, "max", 6)
	meta.PodNames, meta.PodNamesSupplied = nil, false
	got := buildDatadogPerPodQuery(datadogPerPodMetricMemory, meta, "kube_namespace:nudgebee", 1209600)

	assert.NotContains(t, got, ", 1209600)",
		"a bucket as wide as the window silently drops series")
	rollup := datadogPerPodRollupSeconds(meta, 1209600)
	assert.LessOrEqual(t, rollup, datadogCoarsestRollupSeconds,
		"the rollup must stay inside the budget, well under the width that loses series")
	assert.Equal(t,
		fmt.Sprintf("avg:kubernetes.memory.working_set{kube_namespace:nudgebee} by "+
			"{kube_namespace,pod_name,kube_container_name}.rollup(max, %d)", rollup),
		got)
}

// The bucket function is the aggregate's OWN wherever Datadog has it.
//
// Bucketing by average and then reducing on this side does not give the statistic that
// was asked for: a spike shorter than the bucket is averaged away before this side
// ever sees it. Measured on 69 pod incarnations at the same 900s width, `max` over
// max-buckets read 6.7x higher than `max` over avg-buckets, and 21x at the extreme.
//
// A percentile is the one exception, because Datadog has no percentile rollup.
//
// `count` used to be a second exception, bucketed with avg on the reasoning that a count
// cares only whether a bucket exists. It does not: a bucket holding one sample was then
// credited its whole width. Measured on a 2700s bucket, a pod that reported 3 samples
// scored 36 step-equivalent points where it had earned 0.8, against a floor of 50.
func TestDatadogRollupFunc_UsesTheAggregatesOwnFunction(t *testing.T) {
	for _, tc := range []struct{ aggregate, want string }{
		{"max", "max"}, {"min", "min"}, {"sum", "sum"}, {"avg", "avg"},
		{"count", "count"}, {"p50", "avg"}, {"p99", "avg"}, {"p99.9", "avg"},
	} {
		agg, err := parseAggregate(tc.aggregate)
		require.NoErrorf(t, err, "aggregate %q", tc.aggregate)
		assert.Equalf(t, tc.want, datadogRollupFunc(agg),
			"%s must bucket with %s", tc.aggregate, tc.want)
	}
}

// Every aggregate reaches the query as a bounded rollup, so none can get back to the
// window-wide form by a route a future case forgets to check.
func TestBuildDatadogPerPodQuery_NoAggregateEverAsksForTheWholeWindow(t *testing.T) {
	for _, name := range []string{"max", "min", "avg", "sum", "count", "p50", "p99"} {
		meta := datadogPerPodMeta(t, name, 6)
		got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta, "kube_namespace:nudgebee", 1209600)
		assert.NotContainsf(t, got, ", 1209600)", "aggregate %q asked for the whole window", name)
		assert.Containsf(t, got, fmt.Sprintf(".rollup(%s, ", datadogRollupFunc(meta.Aggregate)),
			"aggregate %q", name)
	}
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
	results := datadogNoteUnanswerableKeys(
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
	datadogCollapseOverTime(results, RequestMetadata{Aggregate: p99})

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
// Which keys the collapse touches, and why it is not the per-pod ones for a plain
// aggregate.
//
// This used to assert that only per-pod keys collapse. That was the bug: Datadog's
// source reads neither `instant` nor `step`, so a key without a rollup of its own came
// back at full resolution however the caller asked — cpu_total returned 119 points for
// aggregate=max against Prometheus's 1, and a consumer reading payload[0] got the
// oldest point in the window.
func TestDatadogCollapseOverTime_TouchesEveryKeyThatStillNeedsIt(t *testing.T) {
	p99, err := parseAggregate("p99")
	require.NoError(t, err)
	max, err := parseAggregate("max")
	require.NoError(t, err)

	series := func(key string) []QueryResult {
		return []QueryResult{{QueryKey: key, Payload: []Result{
			{Timestamps: []int64{1, 2, 3}, Values: []float64{1, 2, 3}},
		}}}
	}

	scalar := series("cpu_usage")
	datadogCollapseOverTime(scalar, RequestMetadata{Aggregate: p99})
	assert.Len(t, scalar[0].Payload[0].Values, 1, "a scalar key has no rollup of its own, so it collapses here")

	perPod := series("container_cpu_usage_by_pod")
	datadogCollapseOverTime(perPod, RequestMetadata{Aggregate: max})
	assert.Len(t, perPod[0].Payload[0].Values, 1,
		"a plain aggregate is reduced here too now, because the window-wide rollup it "+
			"used to be pushed down as loses series")
	assert.Equal(t, 3.0, perPod[0].Payload[0].Values[0], "and it is the maximum of them")

	perPodQuantile := series("container_cpu_usage_by_pod")
	datadogCollapseOverTime(perPodQuantile, RequestMetadata{Aggregate: p99})
	assert.Len(t, perPodQuantile[0].Payload[0].Values, 1,
		"a percentile cannot be pushed down, so it is computed here")
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

// Without a pod list there is no series count -- but the WINDOW is still known, and it
// is half the budget calculation. Sizing on a pessimistic pod count keeps the guarantee
// that made this coarse (a churny workload must not return tens of megabytes) while
// letting a short window have the resolution it can afford.
func TestBuildDatadogPerPodQuery_UnknownPodCountStillHonoursTheWindow(t *testing.T) {
	meta := datadogPerPodMeta(t, "p99", 0)

	// Fourteen days genuinely cannot afford the finest rollup even pessimistically,
	// so it coarsens -- but to what the budget requires, not to the maximum.
	got := buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta, "kube_namespace:nudgebee", 1209600)
	assert.Contains(t, got, ".rollup(avg, 2700)")

	// Six hours fits the finest rollup with room to spare at any plausible pod count,
	// and this is where the old code was most wrong: it charged this request the
	// resolution only a fourteen-day one needs. Measured, that read a CPU p99 at 19%
	// of the value Prometheus reports for the same pod.
	got = buildDatadogPerPodQuery(datadogPerPodMetricCPU, meta, "kube_namespace:nudgebee", 21600)
	assert.Contains(t, got, ".rollup(avg, 300)")

	scoped := datadogPerPodMeta(t, "p99", 6)
	got = buildDatadogPerPodQuery(datadogPerPodMetricCPU, scoped, "kube_namespace:nudgebee", 1209600)
	assert.Contains(t, got, ".rollup(avg, 300)", "a known, small pod list buys resolution")
}

// The rollup is NOT chosen per aggregate, and that is deliberate.
//
// `max` looks like it could stay coarse for free -- max of max-buckets is max -- and an
// earlier version of this change did exactly that to save budget. Measured against
// Prometheus, one workload's memory then read 39% of its true value where the fine
// rollup read 100%. Under-reporting memory is the OOM direction, so every aggregate
// gets the same resolution.
func TestDatadogPerPodRollup_DoesNotVaryByAggregate(t *testing.T) {
	var seen []int
	for _, agg := range []string{"max", "min", "avg", "p99", "count"} {
		meta := datadogPerPodMeta(t, agg, 0)
		seen = append(seen, datadogPerPodRollupSeconds(meta, 21600))
	}
	for _, got := range seen[1:] {
		assert.Equal(t, seen[0], got,
			"every aggregate must get the same rollup; a cheaper one for max under-reports memory")
	}
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
	datadogCollapseOverTime(results, RequestMetadata{Aggregate: p99})

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

// `.rollup(count, W)` counts the points Datadog stored in W, which really is a different
// question from "how many of the caller's steps had data" -- this test used to assert
// the opposite for exactly that reason.
//
// The objection was right about the unit and wrong about the remedy. Avoiding Datadog's
// count meant counting BUCKETS instead, which credits a bucket holding one sample with
// its entire width: a pod that reported 3 samples in a 2700s bucket scored 36
// step-equivalent points against a floor of 50, a 45x over-credit.
//
// The unit is now converted rather than avoided. A bucket's value is its raw sample
// count, and the interval between samples is read from the busiest bucket in the
// response (a saturated 2700s bucket holding 135 samples means one every 20s), so
// samples x interval / step lands back in the caller's unit -- and, unlike counting
// buckets, does not change when the bucket width does.
func TestBuildDatadogPerPodQuery_CountAsksDatadogToCount(t *testing.T) {
	count, err := parseAggregate("count")
	require.NoError(t, err)

	meta := RequestMetadata{
		Aggregate:        count,
		PodNames:         []string{"web-1", "web-2"},
		PodNamesSupplied: true,
	}
	q := buildDatadogPerPodQuery("kubernetes.cpu.usage.total", meta, "kube_namespace:shop", 14*24*3600)

	assert.Contains(t, q, ".rollup(count, 300)",
		"the finest rollup two pods over 14 days affords, counting samples not averaging them")
}

// The collapse has to multiply by the SAME rollup the query asked for. Recomputing it
// from the same inputs is the only way that stays true when the budget changes.
func TestDatadogCollapseOverTime_CountIsStepEquivalent(t *testing.T) {
	count, err := parseAggregate("count")
	require.NoError(t, err)

	meta := RequestMetadata{
		Aggregate:        count,
		RangeWindow:      "1209600s", // 14 days
		Step:             "75s",
		PodNames:         []string{"web-1", "web-2"},
		PodNamesSupplied: true,
	}
	require.Equal(t, 300, datadogPerPodRollupSeconds(meta, 14*24*3600),
		"the query and the collapse must agree on the bucket width")

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload: []Result{
			// Twelve 5-minute buckets: one hour of observation.
			{Metric: map[string]string{"pod_name": "web-1"}, Timestamps: seq(12), Values: ones(12)},
			// Same twelve buckets, but the pod only existed for four of them.
			{Metric: map[string]string{"pod_name": "web-2"}, Timestamps: seq(12), Values: onesThenGaps(4, 8)},
		},
	}}
	datadogCollapseOverTime(results, meta)

	require.Len(t, results[0].Payload[0].Values, 1, "one point, as Aggregate promises")
	assert.InDelta(t, 48, results[0].Payload[0].Values[0], 0.001,
		"an hour of data is 48 steps of 75s — the same number Prometheus would count")
	assert.InDelta(t, 16, results[0].Payload[1].Values[0], 0.001,
		"20 minutes is 16 steps; a gap is not observation")
}

// Without a pod list the rollup is whatever the window affords, and the conversion has
// to use the SAME one — otherwise a workload-wide count is out by exactly their ratio.
func TestDatadogCollapseOverTime_CountFollowsTheCoarseRollup(t *testing.T) {
	count, err := parseAggregate("count")
	require.NoError(t, err)

	meta := RequestMetadata{Aggregate: count, RangeWindow: "1209600s", Step: "75s"}
	require.Equal(t, 2700, datadogPerPodRollupSeconds(meta, 14*24*3600))

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload:  []Result{{Timestamps: seq(2), Values: ones(2)}},
	}}
	datadogCollapseOverTime(results, meta)
	assert.InDelta(t, 2*2700/75.0, results[0].Payload[0].Values[0], 0.001,
		"two buckets are two bucket-widths of observation, at whatever width was asked for")
}

// A metadata built without a step (every direct-constructed one, and any caller that
// does not send one) must get Datadog's own number rather than a silent division.
func TestDatadogCollapseOverTime_CountWithoutAStepIsLeftInBuckets(t *testing.T) {
	count, err := parseAggregate("count")
	require.NoError(t, err)

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload:  []Result{{Timestamps: seq(5), Values: ones(5)}},
	}}
	datadogCollapseOverTime(results, RequestMetadata{Aggregate: count, RangeWindow: "86400s"})
	assert.Equal(t, float64(5), results[0].Payload[0].Values[0], "five buckets, uncounted in steps")
}

func seq(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(1_700_000_000 + i*300)
	}
	return out
}

func ones(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

func onesThenGaps(measured, gaps int) []float64 {
	out := ones(measured)
	for i := 0; i < gaps; i++ {
		out = append(out, math.NaN())
	}
	return out
}

// Coverage measures OBSERVATION, not how many buckets came back.
//
// Under a count rollup a bucket's value is how many raw samples it holds, so a pod that
// reported three samples in one 2700s bucket has earned 0.8 step-equivalent points --
// not the 36 that crediting the bucket its whole width would give. Against a floor of
// 50 that is the difference between including a pod on three readings and excluding it.
func TestDatadogCollapseOverTime_CountMeasuresSamplesNotBuckets(t *testing.T) {
	count, err := parseAggregate("count")
	require.NoError(t, err)
	meta := RequestMetadata{Aggregate: count, RangeWindow: "1209600s", Step: "75s"}
	require.Equal(t, 2700, datadogPerPodRollupSeconds(meta, 14*24*3600))

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload: []Result{
			// A saturated bucket: 2700s at one sample every 20s. This is what tells the
			// collapse the submission interval, so it must come from the same response.
			{Timestamps: seq(2), Values: []float64{135, 135}},
			// The thin pod: three readings, in one bucket.
			{Timestamps: seq(1), Values: []float64{3}},
		},
	}}
	datadogCollapseOverTime(results, meta)

	dense := results[0].Payload[0].Values[0]
	thin := results[0].Payload[1].Values[0]

	assert.InDelta(t, 270*20/75.0, dense, 0.01, "270 samples at 20s is 5400s of observation")
	assert.InDelta(t, 3*20/75.0, thin, 0.01, "three readings are three readings")
	assert.Less(t, thin, 50.0, "a pod seen three times must not clear a 50-point floor")
	assert.Greater(t, dense, 50.0, "a pod seen throughout must clear it")
}

// The interval is read from the busiest bucket in the WHOLE result, not per series.
// A pod that lived for a fraction of one bucket would otherwise imply a long interval
// and inflate every pod's coverage.
func TestDatadogSamplePeriod_ComesFromTheBusiestBucketAnywhere(t *testing.T) {
	result := QueryResult{Payload: []Result{
		{Values: []float64{2}},
		{Values: []float64{135, 100}},
		{Values: []float64{1}},
	}}
	assert.InDelta(t, 20.0, datadogSamplePeriod(result, 2700), 0.01)

	// Nothing usable: fall back to the bucket width, which is the old behaviour rather
	// than an invented number.
	empty := QueryResult{Payload: []Result{{Values: []float64{math.NaN()}}}}
	assert.Equal(t, 2700.0, datadogSamplePeriod(empty, 2700))
}

// A count must survive the unit conversion untouched.
//
// Datadog labels a CPU series `nanocore` whatever the rollup, because that is the unit
// of the metric being counted -- not of the count. Scaling it anyway divided a pod's 37
// samples by a billion, and its coverage read 3.7e-8 against a floor of 50, so every
// pod on every Datadog account was excluded from CPU sizing. Memory was unaffected only
// because its unit is `byte`, which is why the failure looked provider-specific rather
// than aggregate-specific.
func TestDatadogNanocoresToCores_LeavesACountAlone(t *testing.T) {
	count, err := parseAggregate("count")
	require.NoError(t, err)
	maxAgg, err := parseAggregate("max")
	require.NoError(t, err)

	newResults := func() []QueryResult {
		return []QueryResult{{
			QueryKey: "container_cpu_usage_by_pod",
			Payload: []Result{{
				Metric: map[string]string{"unit": "nanocore", "pod": "web-1"},
				Values: []float64{37, 136},
			}},
		}}
	}

	counted := newResults()
	datadogNanocoresToCores(counted, RequestMetadata{Aggregate: count})
	assert.Equal(t, []float64{37, 136}, counted[0].Payload[0].Values,
		"sample counts are not nanocores")

	// The conversion still has to happen for an actual quantity, or CPU reads a
	// billion times high beside requests and limits Datadog already gives in cores.
	measured := newResults()
	datadogNanocoresToCores(measured, RequestMetadata{Aggregate: maxAgg})
	assert.InDelta(t, 37e-9, measured[0].Payload[0].Values[0], 1e-18)
	assert.Equal(t, "core", measured[0].Payload[0].Metric["unit"])
}
