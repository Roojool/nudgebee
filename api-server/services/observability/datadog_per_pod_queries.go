package observability

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Per-pod container usage on Datadog, for rightsizing.
//
// Datadog answers these differently from every other provider, in two ways that the
// contract has to absorb rather than expose:
//
//   - **There is no percentile over time.** `p95:`/`p99:` are distribution-metric
//     aggregators for APM traces, not rollups over a gauge, so a percentile of
//     kubernetes.cpu.usage.total has to be computed here from the raw points.
//   - **Resolution is chosen, not given.** Left alone, Datadog rolls a 14-day query
//     up into 2-hour buckets — measured on a live account: 168 points per pod, so a
//     "p99" over eight readings is barely more than a max. An explicit
//     `.rollup(avg, 300)` gives 4,032 points per pod instead, which is a real
//     percentile, and 28.5 MB for a workload with 387 pod incarnations. The rollup
//     is therefore picked from how many pods the caller asked about.
const (
	// datadogPerPodMetricCPU and friends are the gauges Datadog's agent reports.
	datadogPerPodMetricCPU           = "kubernetes.cpu.usage.total"
	datadogPerPodMetricMemory        = "kubernetes.memory.working_set"
	datadogPerPodMetricCPURequest    = "kubernetes.cpu.requests"
	datadogPerPodMetricCPULimit      = "kubernetes.cpu.limits"
	datadogPerPodMetricMemoryRequest = "kubernetes.memory.requests"
	datadogPerPodMetricMemoryLimit   = "kubernetes.memory.limits"

	datadogPerPodPointBudget     = 200_000
	datadogFinestRollupSeconds   = 300
	datadogCoarsestRollupSeconds = 3600
)

// datadogPerPodKeys are answered per pod and container rather than per workload.
var datadogPerPodKeys = map[string]string{
	"container_cpu_usage_by_pod":          datadogPerPodMetricCPU,
	"container_memory_working_set_by_pod": datadogPerPodMetricMemory,
	"container_cpu_request_by_pod":        datadogPerPodMetricCPURequest,
	"container_cpu_limit_by_pod":          datadogPerPodMetricCPULimit,
	"container_memory_request_by_pod":     datadogPerPodMetricMemoryRequest,
	"container_memory_limit_by_pod":       datadogPerPodMetricMemoryLimit,
}

// datadogPerPodUnanswerable are per-pod keys Datadog reports no metric for, and why.
// Kept as a list rather than an absence so the reason reaches the caller: a key that
// is merely missing from the map above would be dropped from the response, which is
// indistinguishable from a workload that used nothing.
var datadogPerPodUnanswerable = map[string]string{
	"container_memory_max_usage_by_pod": "Datadog reports no cgroup high-water mark. " +
		"kubernetes.memory.working_set is a different number — answering with it would size a limit too low.",
}

// datadogRollupSecondsForPods picks the finest rollup whose response stays inside the
// point budget.
//
// points = (window / rollup) * pods, and the budget is what keeps a churny workload
// from pulling tens of megabytes: 387 pod incarnations at 5-minute resolution over 14
// days measured 28.5 MB, while a typical six-pod workload at the same resolution is
// well under a megabyte. Coarsening costs resolution, which the note says; the
// alternative is a response nobody can afford to fetch.
func datadogRollupSecondsForPods(windowSeconds int64, pods int) int {
	if pods < 1 {
		pods = 1
	}
	for rollup := datadogFinestRollupSeconds; rollup <= datadogCoarsestRollupSeconds; rollup *= 3 {
		if (windowSeconds/int64(rollup))*int64(pods) <= datadogPerPodPointBudget {
			return rollup
		}
	}
	return datadogCoarsestRollupSeconds
}

// buildDatadogPerPodQuery renders one per-pod metric, grouped by pod and container.
//
// A plain aggregate is pushed down: rolling up over the WHOLE window collapses each
// series to one point, which is what Aggregate means, and costs one value per pod on
// the wire. A percentile cannot be pushed down, so the query asks for the finest
// resolution the budget allows and datadogCollapseOverTime does the arithmetic.
func buildDatadogPerPodQuery(metric string, meta RequestMetadata, filterStr string, windowSeconds int64) string {
	query := fmt.Sprintf("avg:%s{%s} by {kube_namespace,pod_name,kube_container_name}", metric, datadogPerPodFilter(filterStr, meta))
	switch {
	case meta.Aggregate.Name == "":
		return query
	case meta.Aggregate.IsQuantile():
		// The budget is a count of points, so it needs a count of series. Without a
		// pod list the query is workload-wide and the series count is unknown, so
		// take the coarsest rollup rather than size on a number we do not have — a
		// churny workload is exactly where the guess would be worst.
		rollup := datadogCoarsestRollupSeconds
		if meta.PodNamesSupplied {
			rollup = datadogRollupSecondsForPods(windowSeconds, len(meta.PodNames))
		}
		return fmt.Sprintf("%s.rollup(avg, %d)", query, rollup)
	default:
		// One bucket as wide as the window IS the collapse.
		return fmt.Sprintf("%s.rollup(%s, %d)", query, meta.Aggregate.Name, windowSeconds)
	}
}

// datadogPerPodFilter scopes the query to the caller's exact pod list.
//
// Datadog rejects a scope that mixes comma-separated filters with boolean ones, so
// naming pods means re-joining the whole filter with AND. Verified against the live
// API: `{kube_namespace:x AND (pod_name:a OR pod_name:b)}` returns exactly those two
// pods, while the comma form alongside a boolean does not parse.
//
// Pod names need no quoting: RFC1123 limits them to lower-case alphanumerics, "-" and
// ".", none of which are Datadog scope metacharacters.
func datadogPerPodFilter(filterStr string, meta RequestMetadata) string {
	if !meta.PodNamesSupplied {
		// No list means "every pod under this workload", which is the comma-joined
		// filter every other Datadog key already uses. Left byte-identical.
		return filterStr
	}

	// Split on the comma and trim, not on ", ": a clause added later without the
	// space would otherwise survive into a boolean scope, and Datadog rejects the
	// mix. An empty filter contributes no clause rather than a leading " AND ".
	var clauses []string
	for _, clause := range strings.Split(filterStr, ",") {
		if trimmed := strings.TrimSpace(clause); trimmed != "" {
			clauses = append(clauses, trimmed)
		}
	}

	if len(meta.PodNames) == 0 {
		// The caller resolved the pods and found none. That is an answer, not a
		// missing parameter, so the filter must match nothing — the workload tag
		// alone would hand back every pod and size the workload on pods the caller
		// had already excluded. An unsatisfiable tag returns no series and no error,
		// confirmed against the live API.
		clauses = append(clauses, "pod_name:"+datadogNoPodsSentinel)
	} else {
		clauses = append(clauses, datadogPodNameClause(meta.PodNames))
	}
	return strings.Join(clauses, " AND ")
}

// datadogNoPodsSentinel is a pod name Kubernetes cannot produce (RFC1123 forbids
// underscores). The same literal as the Prometheus builder's, so the two providers
// express "no pods" identically.
const datadogNoPodsSentinel = "__no_pods_resolved__"

// datadogPodNameClause renders the pod list as one OR group, which is the form that
// can sit beside the AND-joined scope filters.
func datadogPodNameClause(pods []string) string {
	terms := make([]string, len(pods))
	for i, pod := range pods {
		terms[i] = "pod_name:" + pod
	}
	if len(terms) == 1 {
		return terms[0]
	}
	return "(" + strings.Join(terms, " OR ") + ")"
}

// datadogCollapseOverTime reduces each series to the requested percentile of its own
// values, over time.
//
// Deliberately not computePercentileFromSeries, which reduces ACROSS series at each
// timestamp — a different question that happens to share the name "p90". This is the
// axis ooPercentile implements for OpenObserve, and the one rightsizing means: how
// hot did THIS pod get.
func datadogCollapseOverTime(results []QueryResult, aggregate ParsedAggregate) {
	if aggregate.Name == "" {
		return
	}
	for i := range results {
		_, isPerPod := datadogPerPodKeys[results[i].QueryKey]
		// A plain aggregate on a per-pod key was already pushed into the query as
		// `.rollup(<agg>, window)`, so the response holds one point and there is
		// nothing left to do. Everything else arrives as a range: Datadog's source
		// reads neither `instant` nor `step`, so a key with no rollup of its own
		// comes back at full resolution however the caller asked for it.
		if isPerPod && !aggregate.IsQuantile() {
			continue
		}
		for j := range results[i].Payload {
			series := &results[i].Payload[j]
			if len(series.Timestamps) == 0 {
				continue
			}
			value, ok := datadogReduceOverTime(series.Values, aggregate)
			if !ok {
				// Every sample was a gap — normal for a pod that lived through part
				// of the window. Still exactly one point, because that is what
				// Aggregate promises and what the callers reading payload[0] expect,
				// carrying NaN (which marshals to null) rather than a zero that would
				// read as a pod measured idle.
				value = math.NaN()
			}
			last := series.Timestamps[len(series.Timestamps)-1]
			series.Timestamps, series.Values = []int64{last}, []float64{value}
		}
	}
}

// datadogReduceOverTime applies the requested aggregate to one series' own values.
//
// Datadog can push a plain aggregate down as a rollup, and does so for the per-pod
// keys where the response would otherwise be enormous. Every other key arrives as a
// range whatever was asked for, so the collapse happens here — otherwise `aggregate`
// silently means nothing on Datadog for those keys, and a consumer reading payload[0]
// gets the oldest point in the window instead of the statistic it asked for.
//
// Reports false when the series holds no finite value, which the caller turns into a
// single NaN rather than a zero.
func datadogReduceOverTime(values []float64, aggregate ParsedAggregate) (float64, bool) {
	if aggregate.IsQuantile() {
		return percentileOverTime(values, aggregate.Quantile)
	}

	finite := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			finite = append(finite, v)
		}
	}
	if len(finite) == 0 {
		return 0, false
	}

	switch aggregate.Name {
	case AggregateMax:
		out := finite[0]
		for _, v := range finite[1:] {
			if v > out {
				out = v
			}
		}
		return out, true
	case AggregateMin:
		out := finite[0]
		for _, v := range finite[1:] {
			if v < out {
				out = v
			}
		}
		return out, true
	case AggregateSum, AggregateAvg:
		total := 0.0
		for _, v := range finite {
			total += v
		}
		if aggregate.Name == AggregateSum {
			return total, true
		}
		return total / float64(len(finite)), true
	case AggregateCount:
		return float64(len(finite)), true
	}
	return 0, false
}

// percentileOverTime is linear interpolation over one series' own values, matching
// the quantile_over_time() the Prometheus path asks the engine for and the
// np.percentile() the Python Datadog service used before this moved server-side.
func percentileOverTime(values []float64, quantile float64) (float64, bool) {
	finite := make([]float64, 0, len(values))
	for _, v := range values {
		// A gap arrives as NaN and is not a measurement; counting it as one is the
		// bug that made every Datadog percentile read low.
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			finite = append(finite, v)
		}
	}
	if len(finite) == 0 {
		return 0, false
	}
	sort.Float64s(finite)
	if len(finite) == 1 {
		return finite[0], true
	}
	pos := quantile * float64(len(finite)-1)
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	if hi >= len(finite) {
		return finite[len(finite)-1], true
	}
	return finite[lo] + (finite[hi]-finite[lo])*(pos-float64(lo)), true
}

// datadogNoteUnanswerablePerPodKeys gives each requested key Datadog cannot answer an
// empty result carrying the reason, rather than leaving it out of the response.
//
// Silence and "used nothing" look identical to a caller, and rightsizing acts on the
// difference: a missing high-water mark must not read as a workload that never
// allocated.
func datadogNoteUnanswerablePerPodKeys(results []QueryResult, requested []string) []QueryResult {
	for _, key := range requested {
		reason, unanswerable := datadogPerPodUnanswerable[key]
		if !unanswerable {
			continue
		}
		results = append(results, QueryResult{QueryKey: key, Note: reason})
	}
	return results
}
