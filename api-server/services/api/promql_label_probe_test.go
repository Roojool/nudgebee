package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The relay does not guarantee ascending timestamps, and the threshold tuner
// reads its step off the first two samples and slices the series into baseline
// and firing windows. An unsorted series is therefore a silently wrong
// suggestion, not an error — which is why the relay parser this replaced sorted.
func TestPromQLSortedSamplesOrdersPairsByTimestamp(t *testing.T) {
	timestamps, values := promQLSortedSamples(
		[]int64{200, 100, 150},
		[]float64{2, 1, 1.5},
	)
	assert.Equal(t, []int64{100, 150, 200}, timestamps)
	assert.Equal(t, []float64{1, 1.5, 2}, values, "values must travel with their own timestamp")
}

func TestPromQLSortedSamplesLeavesOrderedInputAlone(t *testing.T) {
	timestamps, values := promQLSortedSamples([]int64{100, 160}, []float64{1, 2})
	assert.Equal(t, []int64{100, 160}, timestamps)
	assert.Equal(t, []float64{1, 2}, values)
}

// Timestamps and values are meant to be index-aligned; if a backend ever breaks
// that, dropping the unpaired tail is safer than shifting every later value onto
// the wrong timestamp.
func TestPromQLSortedSamplesDropsUnpairedSamples(t *testing.T) {
	timestamps, values := promQLSortedSamples([]int64{100, 160, 220}, []float64{1, 2})
	assert.Equal(t, []int64{100, 160}, timestamps)
	assert.Equal(t, []float64{1, 2}, values)

	empty, emptyValues := promQLSortedSamples(nil, nil)
	assert.Nil(t, empty)
	assert.Nil(t, emptyValues)
}

func TestPromQLSeriesStepSeconds(t *testing.T) {
	assert.Equal(t, 30, promQLSeriesStepSeconds([]int64{100, 130, 160}))
	assert.Equal(t, 60, promQLSeriesStepSeconds([]int64{100}), "a single sample falls back to 60s")
	assert.Equal(t, 60, promQLSeriesStepSeconds(nil))
	assert.Equal(t, 60, promQLSeriesStepSeconds([]int64{100, 100}),
		"a zero gap is not a usable step")
}

// __name__ is not a real dimension: leaving it in would stop a series ever
// matching a firing event's label set.
func TestPromQLSeriesLabelsDropsTheNameMetaLabel(t *testing.T) {
	labels := promQLSeriesLabels(map[string]string{
		"__name__": "pg_cache_hit_ratio",
		"datname":  "temporal_visibility",
		"instance": "db-1",
	})
	assert.Equal(t, map[string]string{"datname": "temporal_visibility", "instance": "db-1"}, labels)
	assert.Empty(t, promQLSeriesLabels(nil))
}
