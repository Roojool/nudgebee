package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// esRightsizingPercentiles is the response shape a per-pod rightsizing query gets
// back: one terms bucket per pod, each carrying the CPU percentiles the strategy
// asks for, a sample count, and when the pod was last seen. Before percentiles were
// read, this whole response was rejected as an unsupported aggregation.
const esRightsizingPercentiles = `{
 "took": 12, "hits": {"total": {"value": 4210, "relation": "eq"}, "hits": []},
 "aggregations": {"pods": {"doc_count_error_upper_bound": 0, "sum_other_doc_count": 0, "buckets": [
   {"key": "api-7d9f", "doc_count": 2105,
    "cpu": {"values": {"92.0": 0.031, "95.0": 0.042, "97.0": 0.055, "99.0": 0.081}},
    "samples": {"value": 2105},
    "last_seen": {"value": 1787800000000.0}},
   {"key": "api-3b2c", "doc_count": 2105,
    "cpu": {"values": {"92.0": 0.028, "95.0": 0.039, "97.0": 0.051, "99.0": 0.077}},
    "samples": {"value": 2105},
    "last_seen": {"value": 1787800000000.0}}]}}}`

func TestParseESMetrics_PercentilesPerPod(t *testing.T) {
	res, stats, err := parseESMetricsHitsWithStats([]byte(esRightsizingPercentiles), 1787800000000)
	require.NoError(t, err, "a percentiles aggregation is a shape we understand")

	// 2 pods x (4 percentiles + samples + last_seen).
	require.Len(t, res, 12)
	assert.Equal(t, 12, stats.SeriesParsed)
	assert.Empty(t, stats.GenericTruncation, "nothing was left out")

	by := map[string]Result{}
	for _, r := range res {
		key := r.Metric["pods"] + "/" + r.Metric["__name__"]
		if stat := r.Metric["statistic"]; stat != "" {
			key += "/" + stat
		}
		by[key] = r
	}

	// The percentile is a label, so one aggregation yields one series per percentile
	// and the caller can tell them apart.
	assert.InDelta(t, 0.081, by["api-7d9f/cpu/99.0"].Values[0], 1e-9)
	assert.InDelta(t, 0.031, by["api-7d9f/cpu/92.0"].Values[0], 1e-9)
	assert.InDelta(t, 0.077, by["api-3b2c/cpu/99.0"].Values[0], 1e-9)
	assert.InDelta(t, 2105, by["api-7d9f/samples"].Values[0], 1e-9)
}

func TestESPercentileValues(t *testing.T) {
	// Keyed (the Elasticsearch default).
	vals, ok := esPercentileValues(map[string]any{"95.0": 1.5, "99.0": 2.5})
	require.True(t, ok)
	assert.Equal(t, map[string]float64{"95.0": 1.5, "99.0": 2.5}, vals)

	// keyed:false — an array of {key, value}.
	vals, ok = esPercentileValues([]any{
		map[string]any{"key": 95.0, "value": 1.5},
		map[string]any{"key": 99.9, "value": 2.5},
	})
	require.True(t, ok)
	assert.Equal(t, map[string]float64{"95": 1.5, "99.9": 2.5}, vals)

	// A percentile over no documents is null: a real answer, not a shape we failed
	// to read, so it is skipped rather than reported as unsupported.
	vals, ok = esPercentileValues(map[string]any{"95.0": nil})
	require.True(t, ok)
	assert.Empty(t, vals)

	_, ok = esPercentileValues("not an aggregation")
	assert.False(t, ok, "an unknown shape is reported, never guessed at")
}

// A terms aggregation that returns fewer buckets than the field has values looks
// complete: the series that came back carry no sign that others were dropped. A
// per-pod query on a workload with more pods than the terms size would silently
// lose some, and size the workload on the pods that happened to come back.
func TestParseESMetrics_TermsTruncationIsReported(t *testing.T) {
	truncated := `{
	 "hits": {"total": {"value": 9000, "relation": "eq"}, "hits": []},
	 "aggregations": {"pods": {"doc_count_error_upper_bound": 0, "sum_other_doc_count": 4400, "buckets": [
	   {"key": "api-7d9f", "doc_count": 2300, "cpu": {"values": {"99.0": 0.081}}}]}}}`

	res, stats, err := parseESMetricsHitsWithStats([]byte(truncated), 1787800000000)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Contains(t, stats.GenericTruncation, "terms aggregation left out buckets")
	assert.Contains(t, stats.GenericTruncation, "4400")
}
