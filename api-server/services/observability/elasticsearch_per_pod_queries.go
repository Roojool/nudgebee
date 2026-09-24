package observability

import (
	"encoding/json"
	"fmt"
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
	if !supported || len(src.GroupBy) == 0 || len(src.GroupBy) > esPerPodMaxGroupLevels {
		return nil
	}

	filters := []any{map[string]any{"exists": map[string]any{"field": src.Field}}}
	if src.Name != "" {
		filters = append(filters, map[string]any{"term": map[string]any{src.NameField: src.Name}})
	}
	filters = append(filters, esSourceScopeFilters(src, meta, scope)...)

	// Nest one terms level per grouping field, innermost first, so the aggregation
	// mirrors GroupBy exactly. Two levels render byte-identically to the shape this
	// replaces; a third appears only where a source asks for one.
	aggs := esPerPodLeafAggs(stat, meta.Aggregate)
	for i := len(src.GroupBy) - 1; i >= 0; i-- {
		aggs = map[string]any{
			esPerPodLevelName(i): map[string]any{
				"terms": map[string]any{"field": src.GroupBy[i], "size": esUtilTermsSize},
				"aggs":  aggs,
			},
		}
	}

	return map[string]any{
		"filter": map[string]any{"bool": map[string]any{"filter": filters}},
		"aggs":   aggs,
	}
}

// esPerPodLeafAggs is what sits under the innermost grouping level.
//
// The statistic, and — only for a count — the first and last timestamp the series
// was seen at. Elasticsearch holds one document per collection period, so
// value_count is a document count, and turning it into the Step-wide intervals the
// contract defines (metrics_aggregate_count.go) needs that period. It is not
// declared anywhere we can read: Metricbeat, the OTLP collector and the OTel-native
// exporter all write different documents, and a customer is free to change the
// interval. So it is measured from the data, which costs two numeric sub-aggregations
// on a bucket that is already being built.
func esPerPodLeafAggs(stat map[string]any, agg ParsedAggregate) map[string]any {
	leaf := map[string]any{"stat": stat}
	if agg.Name == AggregateCount {
		leaf["ts_min"] = map[string]any{"min": map[string]any{"field": esPerPodTimeField}}
		leaf["ts_max"] = map[string]any{"max": map[string]any{"field": esPerPodTimeField}}
	}
	return leaf
}

// esPerPodTimeField is the one field every layout here shares. The date_histogram
// path filters and buckets on it unconditionally for all three document shapes.
const esPerPodTimeField = "@timestamp"

// esPerPodMaxGroupLevels is how deep the collapsed aggregation nests: namespace, pod
// and container is the deepest any per-container key needs.
const esPerPodMaxGroupLevels = 3

// esPerPodLevelName is the aggregation name for grouping level i. Kept as g0/g1/g2 so
// a two-level body is byte-identical to the one this generalises.
func esPerPodLevelName(i int) string { return fmt.Sprintf("g%d", i) }

// esPerPodLevelLabel names the canonical label a grouping field produces. Resolved
// from the source's own field names rather than from the level's position, so adding
// a namespace level above the pod cannot silently relabel the pod as a namespace.
func esPerPodLevelLabel(src esMetricSource, field string) string {
	switch field {
	case src.Fields.Namespace:
		return "namespace"
	case src.Fields.Pod:
		return "pod"
	case src.Fields.Container:
		return "container"
	case src.Fields.Node:
		return "node"
	case src.Fields.PVC:
		// Not a pod dimension, but the per-claim keys use this same grouping walk and
		// a level with no name is dropped from the labels — the series would come back
		// unidentifiable rather than wrong, which is harder to notice.
		return "persistentvolumeclaim"
	}
	return ""
}

// esPerPodResult decodes the collapsed shape. Deliberately separate from
// esUtilSourceResult: that one is built around a date_histogram this path does not
// emit, and decoding into it would silently find no buckets.
// esPerPodResult decodes the collapsed shape. Deliberately separate from
// esUtilSourceResult: that one is built around a date_histogram this path does not
// emit, and decoding into it would silently find no buckets.
//
// Levels are decoded generically rather than as named fields, because the number of
// them follows the source's GroupBy — two for pod and container, three when a
// namespace level is needed to keep same-named pods apart.
type esPerPodResult struct {
	DocCount int64                      `json:"doc_count"`
	Levels   map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps doc_count and hands the rest to the level walker.
func (r *esPerPodResult) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if dc, ok := raw["doc_count"]; ok {
		if err := json.Unmarshal(dc, &r.DocCount); err != nil {
			return err
		}
	}
	r.Levels = raw
	return nil
}

// esPerPodTerms is one terms aggregation: its buckets, and how many documents the
// size cap dropped.
type esPerPodTerms struct {
	SumOtherDocCount int64                        `json:"sum_other_doc_count"`
	Buckets          []map[string]json.RawMessage `json:"buckets"`
}

// esPerPodSeries turns the collapsed response into one single-point series per
// grouping combination — pod, or pod and container, or namespace, pod and container.
//
// The point is stamped at the end of the requested window rather than at a sample
// time: the value describes the whole window, and without a histogram there is no
// bucket whose timestamp would be more truthful.
func esPerPodSeries(res *esPerPodResult, src esMetricSource, scale float64, base map[string]string, meta RequestMetadata) ([]Result, int64) {
	ts := meta.EndMs / 1000
	var leaves []esPerPodLeaf
	var truncated int64

	// walk descends one grouping level at a time, carrying the labels collected so
	// far. The label for a level comes from the source's own field names, so a
	// namespace level above the pod cannot be mistaken for the pod.
	var walk func(level int, node map[string]json.RawMessage, labels map[string]string)
	walk = func(level int, node map[string]json.RawMessage, labels map[string]string) {
		if level >= len(src.GroupBy) {
			if leaf, ok := esPerPodReadLeaf(node, base, labels, scale); ok {
				leaves = append(leaves, leaf)
			}
			return
		}

		raw, ok := node[esPerPodLevelName(level)]
		if !ok {
			// The level is absent, so this source grouped less deeply than declared.
			// Read whatever statistic is here rather than dropping the bucket.
			if leaf, ok := esPerPodReadLeaf(node, base, labels, scale); ok {
				leaves = append(leaves, leaf)
			}
			return
		}

		var terms esPerPodTerms
		if err := json.Unmarshal(raw, &terms); err != nil {
			return
		}
		truncated += terms.SumOtherDocCount

		name := esPerPodLevelLabel(src, src.GroupBy[level])
		for _, bucket := range terms.Buckets {
			var key string
			if err := json.Unmarshal(bucket["key"], &key); err != nil || key == "" {
				continue
			}
			next := make(map[string]string, len(labels)+1)
			for k, v := range labels {
				next[k] = v
			}
			if name != "" {
				next[name] = key
			}
			walk(level+1, bucket, next)
		}
	}

	walk(0, res.Levels, map[string]string{})

	if meta.Aggregate.Name == AggregateCount {
		esPerPodCountAsSteps(leaves, aggregateStepSeconds(meta))
	}

	series := make([]Result, 0, len(leaves))
	for _, leaf := range leaves {
		series = append(series, Result{
			Metric:     leaf.Metric,
			Timestamps: []int64{ts},
			Values:     []float64{leaf.Value},
		})
	}
	if len(series) == 0 {
		return nil, truncated
	}
	return series, truncated
}

// esPerPodLeaf is one grouping combination's answer, before the count conversion.
//
// SpanMs and Value are kept together because the conversion is not per series: the
// collection period is a property of the stream, and the only honest way to read it
// off a set of counts is to compare them (see esPerPodCountAsSteps).
type esPerPodLeaf struct {
	Metric map[string]string
	Value  float64
	SpanMs float64
}

// esPerPodReadLeaf pulls the statistic, and the observed span when one was asked for.
func esPerPodReadLeaf(node map[string]json.RawMessage, base map[string]string, labels map[string]string, scale float64) (esPerPodLeaf, bool) {
	v, ok := esUtilReducedValue(node["stat"])
	if !ok {
		return esPerPodLeaf{}, false
	}
	leaf := esPerPodLeaf{Metric: esPerPodLabels(base, labels), Value: v * scale}
	lo, haveLo := esUtilReducedValue(node["ts_min"])
	hi, haveHi := esUtilReducedValue(node["ts_max"])
	if haveLo && haveHi && hi > lo {
		leaf.SpanMs = hi - lo
	}
	return leaf, true
}

// esPerPodCountAsSteps rewrites document counts as Step-wide intervals, in place.
//
// The collection period is taken as the SHORTEST cadence any series in the response
// shows. A series' own span divided by its gaps is only an upper bound on the period:
// a pod that was restarted, or descheduled for an hour, has the same document count
// spread over a longer span and looks slower than the stream really is. Taking the
// minimum across series reads the period off whichever series was observed without
// interruption, which on a real cluster is most of them.
//
// With nothing to measure from — one document, or one series that never had two — the
// counts are left exactly as Elasticsearch reported them. That is what a caller gets
// today, so an unmeasurable response cannot be made worse by this.
func esPerPodCountAsSteps(leaves []esPerPodLeaf, stepSeconds float64) {
	period := 0.0
	for _, leaf := range leaves {
		// NaN fails every comparison, so it has to be excluded by name or it reaches
		// the division below and poisons the period for every other series.
		if math.IsNaN(leaf.SpanMs) || math.IsNaN(leaf.Value) || leaf.SpanMs <= 0 || leaf.Value < 2 {
			continue
		}
		// N documents spanning S seconds sit N-1 intervals apart.
		p := leaf.SpanMs / 1000 / (leaf.Value - 1)
		if period == 0 || p < period {
			period = p
		}
	}
	if period <= 0 {
		return
	}
	for i := range leaves {
		leaves[i].Value = stepEquivalentCount(leaves[i].Value, period, stepSeconds)
	}
}

// esPerPodLabels names the series by what produced it, overriding the workload-level
// labels the base carries: on a per-container key the identity of the series IS the
// pod and container, and leaving `workload` set would give every series the same one.
func esPerPodLabels(base map[string]string, grouped map[string]string) map[string]string {
	labels := make(map[string]string, len(base)+len(grouped))
	for k, v := range base {
		labels[k] = v
	}
	delete(labels, "workload")
	for k, v := range grouped {
		labels[k] = v
	}
	return labels
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

		series, truncated := esPerPodSeries(&decoded, src, esPerPodScale(src, meta), esUtilSeriesLabels(src, meta), meta)
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

// esPerPodHistogramSeries returns one time series per group from the ordinary
// histogram response.
//
// This is what a per-pod key answers with no Aggregate. Without it the key fell
// through to the summed workload total, which is the same shape cpu_usage returns and
// is indistinguishable from it - a chart labelled "per pod" drawing one workload line.
// The Prometheus builder groups by (container, pod) whatever the aggregate is; only
// the collapse is optional, never the grouping.
//
// The labels come from the SOURCE's GroupBy, not from the position of the level. They
// used to be hardcoded "pod" and "container", which is right only while every per-pod
// source happens to group by exactly those. pvc_used_by_claim groups by namespace and
// claim, so its series came back labelled pod=<namespace>, container=<claim>: correct
// values under names nothing looks for. Volume rightsizing reads
// `persistentvolumeclaim`, found no such label on any series, and produced no
// recommendation for any volume on an Elasticsearch account - with the data present
// and correct in the response the whole time.
func esPerPodHistogramSeries(sr *esUtilSourceResult, src esMetricSource, scale float64, base map[string]string) []Result {
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
			Metric:     esPerPodLabels(base, esPerPodHistogramLabels(src, s.pod, s.container)),
			Timestamps: s.ts,
			Values:     s.values,
		})
	}
	return results
}

// esPerPodHistogramLabels names each grouping level after the field it was grouped by.
//
// The histogram response carries the levels positionally - g0 and g1 - so the names
// have to come from the source that asked for them. esPerPodLevelLabel is the same
// mapping the collapsed path uses, which is why a key answers with one set of labels
// whether or not an aggregate was requested; they disagreed before, and only the
// collapsed half was right.
//
// A level whose field has no name is dropped rather than guessed at: an unidentifiable
// series is easier to notice than a confidently mislabelled one.
func esPerPodHistogramLabels(src esMetricSource, level0, level1 string) map[string]string {
	labels := map[string]string{}
	values := []string{level0, level1}
	for i, value := range values {
		if value == "" || i >= len(src.GroupBy) {
			continue
		}
		if name := esPerPodLevelLabel(src, src.GroupBy[i]); name != "" {
			labels[name] = value
		}
	}
	return labels
}
