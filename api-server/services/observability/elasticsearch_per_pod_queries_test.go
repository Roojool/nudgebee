package observability

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func esPerPodMeta(t *testing.T, aggregate string) RequestMetadata {
	t.Helper()
	parsed, err := parseAggregate(aggregate)
	require.NoError(t, err)
	return RequestMetadata{
		Namespace: "shop", Name: "web", Kind: "deployment",
		Aggregate: parsed, EndMs: 1_700_000_000_000,
	}
}

// Elasticsearch answers the percentile itself, over the raw samples, so there is no
// histogram: one bucket per pod and container, and the statistic inside it. A
// histogram would make the percentile one over step averages, which is a different
// and smoother number.
func TestBuildESPerPodQuery_CollapsesWithoutAHistogram(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)
	require.True(t, plan.PerPod)

	body := bodyAsMap(t, buildESUtilisationAggQuery(plan, esPerPodMeta(t, "p92"), esScopeWorkload, 1000, 2000, 300))

	g0 := jsonPath(t, body, "aggs", "src_0", "aggs", "g0").(map[string]any)
	assert.Equal(t, "kubernetes.pod.name", jsonPath(t, g0, "terms", "field"))
	assert.NotContains(t, g0["aggs"], "ts", "a collapsed answer carries no date_histogram")

	stat := jsonPath(t, g0, "aggs", "stat", "percentiles").(map[string]any)
	assert.Equal(t, "kubernetes.pod.cpu.usage.nanocores", stat["field"])
	// The percentile, not the fraction: Elasticsearch's `percents` is 92, and 0.92
	// would quietly ask for the 0.92nd percentile — a number near the minimum.
	assert.Equal(t, []any{92.0}, stat["percents"])
}

// A container-scoped source groups twice, so the answer is per pod AND container —
// rightsizing sizes a container, not a pod.
func TestBuildESPerPodQuery_GroupsByContainerWhenTheSourceDoes(t *testing.T) {
	plan, ok := esUtilisationMetric("container_memory_working_set_by_pod", esScopeWorkload, true)
	require.True(t, ok)

	meta := esPerPodMeta(t, "max")
	meta.ContainerName = "web"
	body := bodyAsMap(t, buildESUtilisationAggQuery(plan, meta, esScopeWorkload, 1000, 2000, 300))

	g0 := jsonPath(t, body, "aggs", "src_0", "aggs", "g0").(map[string]any)
	assert.Equal(t, "kubernetes.pod.name", jsonPath(t, g0, "terms", "field"))
	g1 := jsonPath(t, g0, "aggs", "g1").(map[string]any)
	assert.Equal(t, "kubernetes.container.name", jsonPath(t, g1, "terms", "field"))
	assert.Equal(t, "kubernetes.container.memory.workingset.bytes",
		jsonPath(t, g1, "aggs", "stat", "max", "field"))
}

// Every statistic the contract accepts maps to an aggregation, or to none at all —
// never to a different one that would answer confidently with the wrong number.
func TestESPerPodStatAgg(t *testing.T) {
	for _, tc := range []struct{ aggregate, esAgg string }{
		{"avg", "avg"}, {"sum", "sum"}, {"max", "max"}, {"min", "min"},
		{"count", "value_count"},
	} {
		parsed, err := parseAggregate(tc.aggregate)
		require.NoError(t, err)
		agg, ok := esPerPodStatAgg(parsed, "f")
		require.True(t, ok, tc.aggregate)
		assert.Contains(t, agg, tc.esAgg)
	}

	p99, err := parseAggregate("p99.9")
	require.NoError(t, err)
	agg, ok := esPerPodStatAgg(p99, "f")
	require.True(t, ok)
	assert.Equal(t, []any{99.9}, jsonPath(t, agg, "percentiles", "percents"))

	// The fraction does not survive the trip back through float64: without rounding
	// p29 asks Elasticsearch for the 28.999999999999996th percentile, and p7 for the
	// 7.000000000000001st.
	for _, tc := range []struct {
		aggregate string
		percent   float64
	}{{"p29", 29}, {"p7", 7}, {"p1", 1}, {"p99.9", 99.9}} {
		parsed, err := parseAggregate(tc.aggregate)
		require.NoError(t, err)
		agg, ok := esPerPodStatAgg(parsed, "f")
		require.True(t, ok)
		assert.Equal(t, []any{tc.percent}, jsonPath(t, agg, "percentiles", "percents"), tc.aggregate)
	}

	_, ok = esPerPodStatAgg(ParsedAggregate{Name: "stddev"}, "f")
	assert.False(t, ok, "an aggregate with no equivalent builds no query")
}

// The pod list must reach the filter. The prefix it replaces matches
// "web-worker-…" as readily as "web-…", which sizes one workload on another's pods.
func TestESPerPodFilter_ScopesToTheExactPodList(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)

	meta := esPerPodMeta(t, "p92")
	meta.PodNames = []string{"web-7d9f-a", "web-7d9f-b"}
	meta.PodNamesSupplied = true

	body := bodyAsMap(t, buildESUtilisationAggQuery(plan, meta, esScopeWorkload, 1000, 2000, 300))
	filters := jsonPath(t, body, "aggs", "src_0", "filter", "bool", "filter").([]any)

	var terms []any
	var havePrefix bool
	for _, f := range filters {
		m := f.(map[string]any)
		if tm, ok := m["terms"].(map[string]any); ok {
			terms, _ = tm["kubernetes.pod.name"].([]any)
		}
		if _, ok := m["prefix"]; ok {
			havePrefix = true
		}
	}
	assert.Equal(t, []any{"web-7d9f-a", "web-7d9f-b"}, terms)
	assert.False(t, havePrefix, "the exact list replaces the prefix, it does not join it")
}

// An empty list is an answer: the caller resolved the pods and found none. It must
// match nothing rather than fall back to the workload prefix.
func TestESPerPodFilter_EmptyListMatchesNothing(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)

	meta := esPerPodMeta(t, "p92")
	meta.PodNames, meta.PodNamesSupplied = []string{}, true

	body := bodyAsMap(t, buildESUtilisationAggQuery(plan, meta, esScopeWorkload, 1000, 2000, 300))
	filters := jsonPath(t, body, "aggs", "src_0", "filter", "bool", "filter").([]any)

	var found bool
	for _, f := range filters {
		m := f.(map[string]any)
		if tm, ok := m["terms"].(map[string]any); ok {
			assert.Equal(t, []any{}, tm["kubernetes.pod.name"], "an empty terms filter matches nothing")
			found = true
		}
		assert.NotContains(t, m, "prefix", "no fallback to the workload's other pods")
	}
	assert.True(t, found)

	// It must also survive the round trip to JSON: a nil slice would marshal as
	// null, which Elasticsearch rejects outright.
	raw, err := json.Marshal(esPodNameValues(nil))
	require.NoError(t, err)
	assert.Equal(t, "[]", string(raw))
}

// One series per pod and container, carrying its own identity rather than the
// workload's, with the source's scale applied.
func TestParseESPerPodResponse(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, true)
	require.True(t, ok)

	body := []byte(`{"aggregations":{"src_0":{"doc_count":40,"g0":{"sum_other_doc_count":7,"buckets":[
		{"key":"web-a","g1":{"buckets":[{"key":"app","stat":{"values":{"92.0":250000000}}}]}},
		{"key":"web-b","g1":{"buckets":[{"key":"app","stat":{"values":{"92.0":500000000}}}]}}
	]}}}}`)

	var resp esUtilAggResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	out, err := parseESPerPodResponse(&resp, plan, esPerPodMeta(t, "p92"))
	require.NoError(t, err)

	require.Len(t, out.Series, 2)
	assert.Equal(t, "web-a", out.Series[0].Metric["pod"])
	assert.Equal(t, "app", out.Series[0].Metric["container"])
	assert.NotContains(t, out.Series[0].Metric, "workload",
		"the series is a pod, not the workload it belongs to")
	// nanocores to cores, the same conversion every other CPU source applies.
	assert.InDelta(t, 0.25, out.Series[0].Values[0], 1e-9)
	assert.InDelta(t, 0.5, out.Series[1].Values[0], 1e-9)
	// One point, stamped at the window end: it describes the window, and there is no
	// bucket whose time would be more truthful.
	assert.Equal(t, []int64{1_700_000_000}, out.Series[0].Timestamps)
	assert.EqualValues(t, 40, out.DocsMatched)
	assert.EqualValues(t, 7, out.TruncatedDocs)
}

// A pod-level source groups once, so there is no container bucket to read.
func TestParseESPerPodResponse_PodLevelSourceHasNoContainer(t *testing.T) {
	plan, ok := esUtilisationMetric("container_memory_working_set_by_pod", esScopeWorkload, false)
	require.True(t, ok)

	body := []byte(`{"aggregations":{"src_0":{"doc_count":2,"g0":{"buckets":[
		{"key":"web-a","stat":{"value":1048576}}]}}}}`)

	var resp esUtilAggResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	out, err := parseESPerPodResponse(&resp, plan, esPerPodMeta(t, "max"))
	require.NoError(t, err)

	require.Len(t, out.Series, 1)
	assert.Equal(t, "web-a", out.Series[0].Metric["pod"])
	assert.NotContains(t, out.Series[0].Metric, "container")
	assert.InDelta(t, 1048576, out.Series[0].Values[0], 1e-9)
}

// Elasticsearch ships no cgroup high-water mark, so the key is unsupported and says
// so. Answering with the working set instead would size a limit too low.
func TestESPerPod_HighWaterMarkIsUnsupported(t *testing.T) {
	_, ok := esUtilisationMetric("container_memory_max_usage_by_pod", esScopeWorkload, false)
	assert.False(t, ok, "reported as unsupported, not answered with a different metric")

	note := esUtilUnsupportedNote("container_memory_max_usage_by_pod", esScopeWorkload)
	assert.Contains(t, note, "container_memory_max_usage_by_pod")
}

// Node and cluster scope group by node, not pod, so answering there would count
// nodes and call them pods.
func TestESPerPod_NotAnsweredAtNodeScope(t *testing.T) {
	for _, scope := range []esUtilScope{esScopeNode, esScopeCluster} {
		_, ok := esUtilisationMetric("container_cpu_usage_by_pod", scope, false)
		assert.False(t, ok, string(scope))
	}
	_, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopePod, false)
	assert.True(t, ok, "pod scope groups by pod, so it answers")
}

// Without an aggregate the key keeps the ordinary histogram body. Only the collapse
// is optional; the per-pod grouping is not.
func TestBuildESPerPodQuery_NoAggregateKeepsTheHistogram(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)

	meta := RequestMetadata{Namespace: "shop", Name: "web", Kind: "deployment"}
	body := bodyAsMap(t, buildESUtilisationAggQuery(plan, meta, esScopeWorkload, 1000, 2000, 300))

	ts := jsonPath(t, body, "aggs", "src_0", "aggs", "ts", "date_histogram").(map[string]any)
	assert.Equal(t, "300s", ts["fixed_interval"])
}

// The grouping survives without an Aggregate. This is the case that was wrong: the
// key fell through to the summed workload total, which is the same shape cpu_usage
// returns, so a chart labelled "per pod" drew one workload line with no error.
func TestParseESPerPod_NoAggregateStillReturnsOneSeriesPerPod(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)

	// Two pods, two buckets each, plus the workload total the summed path would have
	// returned instead.
	body := []byte(`{"aggregations":{"src_0":{"doc_count":8,"ts":{"buckets":[
		{"key":1700000000000,"total":{"value":3000000000},"g0":{"buckets":[
			{"key":"web-a","v":{"value":1000000000}},{"key":"web-b","v":{"value":2000000000}}]}},
		{"key":1700000060000,"total":{"value":7000000000},"g0":{"buckets":[
			{"key":"web-a","v":{"value":3000000000}},{"key":"web-b","v":{"value":4000000000}}]}}
	]}}}}`)

	meta := RequestMetadata{Namespace: "shop", Name: "web", Kind: "deployment"}
	out, err := parseESUtilisationAggs(body, plan, meta)
	require.NoError(t, err)

	require.Len(t, out.Series, 2, "one series per pod, not one for the workload")
	assert.Equal(t, "web-a", out.Series[0].Metric["pod"])
	assert.Equal(t, "web-b", out.Series[1].Metric["pod"])

	// A time series, not a collapse: both buckets survive.
	assert.Equal(t, []int64{1700000000, 1700000060}, out.Series[0].Timestamps)
	assert.InDelta(t, 1.0, out.Series[0].Values[0], 1e-9)
	assert.InDelta(t, 3.0, out.Series[0].Values[1], 1e-9)
	// The summed total (3.0 then 7.0) must not be what came back.
	assert.NotEqual(t, 3.0, out.Series[0].Values[0])
}

// The same, for a source that groups by pod AND container.
func TestParseESPerPod_NoAggregateSplitsByContainer(t *testing.T) {
	plan, ok := esUtilisationMetric("container_memory_working_set_by_pod", esScopeWorkload, true)
	require.True(t, ok)

	body := []byte(`{"aggregations":{"src_0":{"doc_count":4,"ts":{"buckets":[
		{"key":1700000000000,"total":{"value":300},"g0":{"buckets":[
			{"key":"web-a","gsum":{"value":300},"g1":{"buckets":[
				{"key":"app","v":{"value":200}},{"key":"sidecar","v":{"value":100}}]}}]}}
	]}}}}`)

	meta := RequestMetadata{Namespace: "shop", Name: "web", Kind: "deployment", ContainerName: "app"}
	out, err := parseESUtilisationAggs(body, plan, meta)
	require.NoError(t, err)

	require.Len(t, out.Series, 2)
	assert.Equal(t, "app", out.Series[0].Metric["container"])
	assert.Equal(t, "sidecar", out.Series[1].Metric["container"])
	assert.InDelta(t, 200, out.Series[0].Values[0], 1e-9)
}

// A count is a count. value_count on a CPU field multiplied by nanocoresToCores
// reports 2.4e-7 samples, which is the mistake the histogram path already guards.
func TestESPerPod_CountIsNotScaledIntoTheSourceUnits(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)
	require.InDelta(t, 1e-9, plan.Sources[0].Scale, 1e-18, "the CPU source really does scale")

	body := []byte(`{"aggregations":{"src_0":{"doc_count":240,"g0":{"buckets":[
		{"key":"web-a","stat":{"value":240}}]}}}}`)

	var resp esUtilAggResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	out, err := parseESPerPodResponse(&resp, plan, esPerPodMeta(t, "count"))
	require.NoError(t, err)

	require.Len(t, out.Series, 1)
	assert.InDelta(t, 240, out.Series[0].Values[0], 1e-9, "240 samples, not 2.4e-7")
}

// Source order is preference order. A container-level source always yields at least
// as many series as a pod-level one, so picking the biggest answer would split by
// container even when no container was asked for.
func TestParseESPerPod_TakesTheFirstSourceWithData(t *testing.T) {
	plan, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)
	require.Greater(t, len(plan.Sources), 1)

	// src_0 (the preferred pod-level source) answers with one series; src_1 answers
	// with three.
	body := []byte(`{"aggregations":{
		"src_0":{"doc_count":2,"g0":{"buckets":[{"key":"web-a","stat":{"value":1}}]}},
		"src_1":{"doc_count":9,"g0":{"buckets":[
			{"key":"web-a","stat":{"value":1}},{"key":"web-b","stat":{"value":2}},{"key":"web-c","stat":{"value":3}}]}}}}`)

	var resp esUtilAggResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	out, err := parseESPerPodResponse(&resp, plan, esPerPodMeta(t, "max"))
	require.NoError(t, err)

	assert.Equal(t, 0, out.SourceIdx, "the preferred source answered, so it wins")
	assert.Len(t, out.Series, 1)
	assert.EqualValues(t, 11, out.DocsMatched, "every candidate still counts toward docs searched")
}

// Replica counts come from a dataset with no pod field. Hanging a pod filter on it
// would match nothing and report zero replicas rather than leaving it unfiltered.
func TestESPodNamesFilter_SkipsWorkloadScopedSources(t *testing.T) {
	plan, ok := esUtilisationMetric("replica_defined", esScopeWorkload, false)
	require.True(t, ok)
	require.True(t, plan.Sources[0].WorkloadScoped)

	meta := RequestMetadata{Namespace: "shop", PodNames: []string{"web-a"}, PodNamesSupplied: true}
	body := bodyAsMap(t, buildESUtilisationAggQuery(plan, meta, esScopeWorkload, 1000, 2000, 300))

	filters := jsonPath(t, body, "aggs", "src_0", "filter", "bool", "filter").([]any)
	for _, f := range filters {
		assert.NotContains(t, f.(map[string]any), "terms",
			"a workload-level source takes no pod filter")
	}
}
