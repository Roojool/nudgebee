package observability

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The OTel family is a GAUGE in cores; cAdvisor's is a counter in seconds. Swapping the
// name into the counter query would rate() a gauge and answer near-zero on every pod --
// a plausible small number, which is the worst shape a wrong answer can take.
func TestOpenObservePerPodQuery_RatesOnlyTheCounterFamily(t *testing.T) {
	meta := RequestMetadata{Namespace: "nudgebee", Name: "workflow-server", ContainerName: "workflow-server"}

	cadvisor := OpenObservePerPodCandidates[ooRolePodCPUUsage][0]
	q := buildOpenObservePerPodQuery(cadvisor, meta, "300s")
	assert.Contains(t, q, "rate(container_cpu_usage_seconds_total{")
	assert.Contains(t, q, "by (namespace, pod, container)")

	otel := OpenObservePerPodCandidates[ooRolePodCPUUsage][1]
	q = buildOpenObservePerPodQuery(otel, meta, "300s")
	assert.NotContains(t, q, "rate(", "the OTel family is already in cores")
	assert.Contains(t, q, "sum by (k8s_namespace_name, k8s_pod_name, k8s_container_name)",
		"grouping must use the family's own label spelling, or every pod collapses into one series")
}

// Each family carries its own label spelling, and the scope must be written in it.
func TestOpenObservePerPodQuery_ScopesInTheFamilysOwnLabels(t *testing.T) {
	meta := RequestMetadata{Namespace: "nudgebee", Name: "workflow-server", ContainerName: "app"}
	q := buildOpenObservePerPodQuery(OpenObservePerPodCandidates[ooRolePodMemWSet][1], meta, "300s")
	assert.Contains(t, q, `k8s_namespace_name="nudgebee"`)
	assert.Contains(t, q, `k8s_pod_name=~"workflow-server-.*"`)
	assert.Contains(t, q, `k8s_container_name="app"`)
	assert.NotContains(t, q, `namespace="nudgebee"`, "cAdvisor labels must not leak into an OTel selector")
}

// An exact pod list is the caller's own answer and beats the prefix, which also matches
// a sibling workload whose name extends this one.
func TestOpenObservePerPodQuery_ExactPodListBeatsThePrefix(t *testing.T) {
	meta := RequestMetadata{
		Namespace: "nudgebee", Name: "services-server", ContainerName: "services-server",
		PodNames: []string{"services-server-1", "services-server-2"}, PodNamesSupplied: true,
	}
	q := buildOpenObservePerPodQuery(OpenObservePerPodCandidates[ooRolePodCPUUsage][0], meta, "300s")
	assert.Contains(t, q, `pod=~"services-server-1|services-server-2"`)
	assert.NotContains(t, q, `pod=~"services-server-.*"`)
}

// The probe order is the contract: cAdvisor first, so an instance scraping it needs no
// translation at all, and the memory fallback that is NOT a working set comes last.
func TestOpenObservePerPodCandidates_ProbeOrderIsDeliberate(t *testing.T) {
	cpu := OpenObservePerPodCandidates[ooRolePodCPUUsage]
	assert.Equal(t, "container_cpu_usage_seconds_total", cpu[0].Family)
	assert.True(t, cpu[0].Counter)
	assert.False(t, cpu[1].Counter, "the OTel gauge must not be rated")

	mem := OpenObservePerPodCandidates[ooRolePodMemWSet]
	assert.Equal(t, "container_memory_working_set_bytes", mem[0].Family)
	assert.Equal(t, "container_memory_working_set", mem[1].Family)
	assert.Equal(t, "container_memory_usage", mem[2].Family,
		"usage counts page cache a working set excludes, so it is the last resort")
	for _, c := range mem {
		assert.False(t, c.Counter, "every memory family here is a gauge")
	}
}

// The aggregate must be APPLIED, not just requested.
//
// The first revision of this file asked for an instant query and returned whatever it
// got. One number came back, every shape check passed, and it was the value at the
// window's end -- measured 98.6% below the window's actual peak, with max and avg
// identical. That is the failure this whole suite exists to catch, so it is pinned here.
func TestOOReducePerPod_AppliesTheStatisticRatherThanTakingTheLastPoint(t *testing.T) {
	series := []Result{{
		Metric:     map[string]string{"pod": "web-1"},
		Timestamps: []int64{1, 2, 3, 4},
		Values:     []float64{0.01, 0.40, 0.02, 0.01}, // the peak is NOT last
	}}

	maxAgg, err := parseAggregate("max")
	require.NoError(t, err)
	avgAgg, err := parseAggregate("avg")
	require.NoError(t, err)

	gotMax := ooReducePerPod(series, maxAgg)
	gotAvg := ooReducePerPod(series, avgAgg)

	require.Len(t, gotMax, 1)
	require.Len(t, gotMax[0].Values, 1, "an aggregate promises exactly one point")
	assert.InDelta(t, 0.40, gotMax[0].Values[0], 1e-9, "max must find the peak, not the last sample")
	assert.InDelta(t, 0.11, gotAvg[0].Values[0], 1e-9)
	assert.NotEqual(t, gotMax[0].Values[0], gotAvg[0].Values[0],
		"identical max and avg is how an ignored aggregate presents")
}

// A series of nothing but gaps is still one point, and it is null -- not a zero that
// reads as a measured idle pod.
func TestOOReducePerPod_AllGapSeriesIsNullNotZero(t *testing.T) {
	maxAgg, err := parseAggregate("max")
	require.NoError(t, err)
	got := ooReducePerPod([]Result{{
		Metric:     map[string]string{"pod": "web-1"},
		Timestamps: []int64{1, 2},
		Values:     []float64{math.NaN(), math.NaN()},
	}}, maxAgg)
	require.Len(t, got, 1)
	require.Len(t, got[0].Values, 1)
	assert.True(t, math.IsNaN(got[0].Values[0]), "a gap is not a zero")
}

// Nine keys are two measurements asked differently. Reducing one fetch means they cannot
// disagree with each other, which they can when each is its own query.
func TestOOAcrossSeries_ReducesAcrossPodsNotOverTime(t *testing.T) {
	series := []Result{
		{Metric: map[string]string{"namespace": "ns", "pod": "a", "container": "app"},
			Timestamps: []int64{10, 20}, Values: []float64{1, 2}},
		{Metric: map[string]string{"namespace": "ns", "pod": "b", "container": "app"},
			Timestamps: []int64{10, 20}, Values: []float64{4, 3}},
	}

	sum := ooAcrossSeries(series, ooAcrossSum)
	require.Len(t, sum, 1, "the workload total is one series")
	assert.Equal(t, []float64{5, 5}, sum[0].Values, "sum ACROSS pods at each instant")

	mx := ooAcrossSeries(series, ooAcrossMax)
	assert.Equal(t, []float64{4, 3}, mx[0].Values,
		"the busiest pod can change between instants; this is not a max over time")

	cnt := ooAcrossSeries(series, ooAcrossCount)
	assert.Equal(t, []float64{2, 2}, cnt[0].Values)
}

// Timestamps are matched by VALUE. A pod created later starts its series later, and
// zipping positionally pairs its first sample against another pod's tenth.
func TestOOAcrossSeries_AlignsOnTimestampNotIndex(t *testing.T) {
	series := []Result{
		{Metric: map[string]string{"pod": "old"}, Timestamps: []int64{10, 20, 30}, Values: []float64{1, 1, 1}},
		{Metric: map[string]string{"pod": "new"}, Timestamps: []int64{30}, Values: []float64{5}},
	}
	sum := ooAcrossSeries(series, ooAcrossSum)
	require.Len(t, sum, 1)
	assert.Equal(t, []int64{10, 20, 30}, sum[0].Timestamps)
	assert.Equal(t, []float64{1, 1, 6}, sum[0].Values,
		"the late pod contributes only at the instant it existed")
}

// cpu_usage_by_pod carries namespace and pod but NOT container -- measured against what
// Prometheus answers for the same key. A two-container pod must be ONE series, or a
// consumer keying on pod pairs one container's value against the other's.
func TestOOAcrossSeries_PerPodFoldsContainersTogether(t *testing.T) {
	series := []Result{
		{Metric: map[string]string{"namespace": "ns", "pod": "a", "container": "app"},
			Timestamps: []int64{10}, Values: []float64{1}},
		{Metric: map[string]string{"namespace": "ns", "pod": "a", "container": "sidecar"},
			Timestamps: []int64{10}, Values: []float64{2}},
	}
	perPod := ooAcrossSeries(series, ooAcrossPerPod)
	require.Len(t, perPod, 1, "one pod, one series")
	assert.Equal(t, []float64{3}, perPod[0].Values, "its containers sum")
	assert.NotContains(t, perPod[0].Metric, "container")

	// The per-container family keeps them apart, because that IS its question.
	perCont := ooAcrossSeries(series, ooAcrossPerCont)
	assert.Len(t, perCont, 2)
}

// The fold runs BEFORE label canonicalisation, so it must read the spelling the series
// actually carries. Reading only the canonical name found "" on every OTel series and
// folded the whole workload into ONE -- which still plotted, as the workload total under
// a per-pod key's name. The unit test that missed it used canonical labels in its fixture.
func TestOOFoldContainers_GroupsOtelLabelledSeriesToo(t *testing.T) {
	otel := []Result{
		{Metric: map[string]string{"k8s_namespace_name": "ns", "k8s_pod_name": "a", "k8s_container_name": "app"},
			Timestamps: []int64{10}, Values: []float64{1}},
		{Metric: map[string]string{"k8s_namespace_name": "ns", "k8s_pod_name": "b", "k8s_container_name": "app"},
			Timestamps: []int64{10}, Values: []float64{2}},
	}
	got := ooAcrossSeries(otel, ooAcrossPerPod)
	require.Len(t, got, 2, "two pods must stay two series whatever the label spelling")
	assert.Equal(t, "a", got[0].Metric["pod"])
	assert.Equal(t, "ns", got[0].Metric["namespace"])
	assert.Equal(t, []float64{1}, got[0].Values)
	assert.Equal(t, []float64{2}, got[1].Values)
}

// A series with no points has nothing to reduce and no timestamp to anchor a point to.
//
// Without the guard the reducer emitted one NaN value against zero timestamps, leaving the
// two slices mismatched — which breaks any caller that zips them, and is a worse answer
// than "this pod reported nothing". datadogCollapseOverTime guards the same case; this
// reducer was missing it. Raised in review on #39340.
func TestOOReducePerPod_EmptySeriesKeepsBothSlicesEmpty(t *testing.T) {
	maxAgg, err := parseAggregate("max")
	require.NoError(t, err)

	got := ooReducePerPod([]Result{
		{Metric: map[string]string{"pod": "never-reported"}},
		{Metric: map[string]string{"pod": "has-data"}, Timestamps: []int64{10, 20}, Values: []float64{1, 4}},
	}, maxAgg)

	require.Len(t, got, 2, "an empty series is still a series -- the pod exists")
	assert.Empty(t, got[0].Values)
	assert.Empty(t, got[0].Timestamps)
	assert.Len(t, got[0].Values, len(got[0].Timestamps), "the two slices must agree in length")
	assert.NotNil(t, got[0].Values, "empty, not nil, so it serialises as [] and not null")
	assert.NotNil(t, got[0].Timestamps)

	// The series that does have data is unaffected.
	assert.Equal(t, []float64{4}, got[1].Values)
	assert.Len(t, got[1].Timestamps, 1)
}
