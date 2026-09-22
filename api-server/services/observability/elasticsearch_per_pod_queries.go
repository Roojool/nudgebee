package observability

import (
	"encoding/json"
	"math"
)

// Per-pod container usage on Elasticsearch, for rightsizing.
//
// Elasticsearch is the one phase-1 provider that answers this natively. Prometheus
// computes the percentile in the database and Datadog cannot compute one at all, so
// api-server fetches and reduces; here the aggregation IS the answer, over the raw
// samples, and no arithmetic happens in Go.
//
// The collapsed form carries no date_histogram. A per-pod statistic needs one bucket
// per pod, not one per pod per step, so dropping the histogram is both cheaper and
// more exact: the percentile is taken over the pod's actual samples rather than over
// averages of them. It is also why `step` has no meaning on this path.

// esPerPodStatAgg renders the requested statistic as the aggregation that answers it.
//
// Returns false for an aggregate Elasticsearch has no aggregation for, so the caller
// can say the combination is unsupported rather than substitute a different number.
func esPerPodStatAgg(agg ParsedAggregate, field string) (map[string]any, bool) {
	if agg.IsQuantile() {
		// percents takes the percentile itself, not a fraction: this is the one place
		// the two conventions meet, and passing the fraction would quietly ask for
		// the 0.92nd percentile, a number near the minimum.
		//
		// Rounded because the fraction does not survive the trip back: 0.29*100 is
		// 28.999999999999996 in float64, and 0.07*100 is 7.000000000000001. Either
		// would go into the query verbatim and come back keyed by that same string.
		// One decimal place is the contract's own granularity (p1 to p99.9).
		percent := math.Round(agg.Quantile*1000) / 10
		return map[string]any{
			"percentiles": map[string]any{"field": field, "percents": []any{percent}},
		}, true
	}
	named := map[string]string{
		"avg": "avg", "sum": "sum", "max": "max", "min": "min", "count": "value_count",
	}

	esAgg, ok := named[agg.Name]
	if !ok {
		return nil, false
	}
	return map[string]any{esAgg: map[string]any{"field": field}}, true
}

// esPerPodCollapsedAgg renders one source as "the statistic per pod, and per
// container when the source carries one", with no histogram.
//
// Returns nil when the source cannot answer per pod — the same contract as
// esSourceAgg, so an unusable candidate degrades to the next one rather than
// answering confidently with the wrong grouping.
func esPerPodCollapsedAgg(src esMetricSource, meta RequestMetadata, scope esUtilScope) map[string]any {
	stat, supported := esPerPodStatAgg(meta.Aggregate, src.Field)
	if !supported || len(src.GroupBy) == 0 || len(src.GroupBy) > 2 {
		return nil
	}

	filters := []any{map[string]any{"exists": map[string]any{"field": src.Field}}}
	if src.Name != "" {
		filters = append(filters, map[string]any{"term": map[string]any{src.NameField: src.Name}})
	}
	filters = append(filters, esSourceScopeFilters(src, meta, scope)...)

	inner := map[string]any{"stat": stat}
	if len(src.GroupBy) == 2 {
		inner = map[string]any{
			"g1": map[string]any{
				"terms": map[string]any{"field": src.GroupBy[1], "size": esUtilTermsSize},
				"aggs":  map[string]any{"stat": stat},
			},
		}
	}

	return map[string]any{
		"filter": map[string]any{"bool": map[string]any{"filter": filters}},
		"aggs": map[string]any{
			"g0": map[string]any{
				"terms": map[string]any{"field": src.GroupBy[0], "size": esUtilTermsSize},
				"aggs":  inner,
			},
		},
	}
}

// esPerPodResult decodes the collapsed shape. Deliberately separate from
// esUtilSourceResult: that one is built around a date_histogram this path does not
// emit, and decoding into it would silently find no buckets.
type esPerPodResult struct {
	DocCount int64 `json:"doc_count"`
	G0       struct {
		SumOtherDocCount int64           `json:"sum_other_doc_count"`
		Buckets          []esPerPodGroup `json:"buckets"`
	} `json:"g0"`
}

type esPerPodGroup struct {
	Key  string          `json:"key"`
	Stat json.RawMessage `json:"stat"`
	G1   struct {
		Buckets []struct {
			Key  string          `json:"key"`
			Stat json.RawMessage `json:"stat"`
		} `json:"buckets"`
	} `json:"g1"`
}

// parseESPerPodResponse reads the collapsed response, taking the first candidate
// source that produced any series.
//
// Declared order, not whichever source emitted the most: the source lists are ordered
// by preference (pod-level first, unless the request names a container), and a
// container-level source always produces at least as many series as a pod-level one
// because pods x containers >= pods. Picking on count would silently invert that
// preference and split by container when no container was asked for.
func parseESPerPodResponse(resp *esUtilAggResponse, m esUtilMetric, meta RequestMetadata) (esUtilOutcome, error) {
	best := esUtilOutcome{SourceIdx: -1}
	var matched int64

	for i, src := range m.Sources {
		raw, ok := resp.Aggregations[esSourceAggName(i)]
		if !ok {
			continue
		}
		var decoded esPerPodResult
		if err := json.Unmarshal(raw, &decoded); err != nil {
			continue
		}
		// Every candidate counts toward "documents searched" even once a winner is
		// settled, because that number describes the request, not the source.
		matched += decoded.DocCount
		if len(best.Series) > 0 {
			continue
		}

		series, truncated := esPerPodSeries(&decoded, esPerPodScale(src, meta), esUtilSeriesLabels(src, meta), meta.EndMs)
		if len(series) == 0 {
			continue
		}
		best = esUtilOutcome{Series: series, SourceIdx: i, TruncatedDocs: truncated}
	}

	best.DocsMatched = matched
	return best, nil
}

// esPodNameValues renders the pod list for a terms filter. A nil slice would marshal
// as JSON null, which Elasticsearch rejects, so an empty list becomes an empty array:
// a terms filter over no values matches nothing, which is exactly what "the caller
// resolved the pods and found none" means.
func esPodNameValues(pods []string) []string {
	if pods == nil {
		return []string{}
	}
	return pods
}

// esPerPodSeries turns the collapsed response into one single-point series per pod,
// or per pod and container when the source groups by both.
//
// The point is stamped at the end of the requested window rather than at a sample
// time: the value describes the whole window, and without a histogram there is no
// bucket whose timestamp would be more truthful.
func esPerPodSeries(res *esPerPodResult, scale float64, base map[string]string, endMs int64) ([]Result, int64) {
	ts := endMs / 1000
	series := make([]Result, 0, len(res.G0.Buckets))
	truncated := res.G0.SumOtherDocCount

	for _, pod := range res.G0.Buckets {
		if pod.Key == "" {
			continue
		}
		if len(pod.G1.Buckets) == 0 {
			if v, ok := esUtilReducedValue(pod.Stat); ok {
				series = append(series, Result{
					Metric:     esPerPodLabels(base, pod.Key, ""),
					Timestamps: []int64{ts},
					Values:     []float64{v * scale},
				})
			}
			continue
		}
		for _, container := range pod.G1.Buckets {
			v, ok := esUtilReducedValue(container.Stat)
			if !ok || container.Key == "" {
				continue
			}
			series = append(series, Result{
				Metric:     esPerPodLabels(base, pod.Key, container.Key),
				Timestamps: []int64{ts},
				Values:     []float64{v * scale},
			})
		}
	}
	return series, truncated
}

// esPerPodScale is the factor a per-pod value is reported in.
//
// A count is a count, never a quantity in the source's units: value_count on a CPU
// field would otherwise be multiplied by nanocoresToCores and report 2.4e-7 samples.
// The histogram path carries the same carve-out for pod_count.
func esPerPodScale(src esMetricSource, meta RequestMetadata) float64 {
	if meta.Aggregate.Name == "count" {
		return 1
	}
	return src.Scale
}

// esPerPodHistogramSeries returns one time series per pod (and container) from the
// ordinary histogram response.
//
// This is what a per-pod key answers with no Aggregate. Without it the key fell
// through to the summed workload total, which is the same shape cpu_usage returns and
// is indistinguishable from it - a chart labelled "per pod" drawing one workload line.
// The Prometheus builder groups by (container, pod) whatever the aggregate is; only
// the collapse is optional, never the grouping.
func esPerPodHistogramSeries(sr *esUtilSourceResult, scale float64, base map[string]string) []Result {
	type series struct {
		pod, container string
		ts             []int64
		values         []float64
	}
	byGroup := map[string]*series{}
	var order []string

	add := func(pod, container string, v *float64, at int64) {
		if v == nil || pod == "" {
			return
		}
		key := pod + "\x00" + container
		s, seen := byGroup[key]
		if !seen {
			s = &series{pod: pod, container: container}
			byGroup[key] = s
			order = append(order, key)
		}
		s.ts = append(s.ts, at)
		s.values = append(s.values, *v*scale)
	}

	for _, b := range sr.TS.Buckets {
		at := b.Key / 1000
		for _, g := range b.G0.Buckets {
			if len(g.G1.Buckets) == 0 {
				add(g.Key, "", g.V.Value, at)
				continue
			}
			for _, c := range g.G1.Buckets {
				add(g.Key, c.Key, c.V.Value, at)
			}
		}
	}

	results := make([]Result, 0, len(order))
	for _, key := range order {
		s := byGroup[key]
		results = append(results, Result{
			Metric:     esPerPodLabels(base, s.pod, s.container),
			Timestamps: s.ts,
			Values:     s.values,
		})
	}
	return results
}

// esPerPodLabels names the series by the pod (and container) that produced it,
// overriding the workload-level labels the base carries: on a per-pod key the
// identity of the series IS the pod, and leaving `pod` set to the workload name
// would give every series the same identity.
func esPerPodLabels(base map[string]string, pod, container string) map[string]string {
	labels := make(map[string]string, len(base)+2)
	for k, v := range base {
		labels[k] = v
	}
	labels["pod"] = pod
	delete(labels, "workload")
	if container != "" {
		labels["container"] = container
	}
	return labels
}
