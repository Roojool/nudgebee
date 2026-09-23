package observability

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The floor a count is checked against is written in steps. A provider that answers
// in coarser readings has to say how much time those readings cover, or the same
// setting means a different thing on every account.
func TestStepEquivalentCount_ReadsCoverageNotStorage(t *testing.T) {
	// What dev's Datadog returned: 2-hour buckets over a 14-day window.
	assert.InDelta(t, 8*7200/75.0, stepEquivalentCount(8, 7200, 75), 0.001,
		"8 two-hour buckets are 16 hours of observation, which is 768 steps of 75s")

	// Elasticsearch's Metricbeat default: one document every 10s. 360 documents is
	// an hour, which is 48 steps — under the 50-step floor, correctly.
	assert.InDelta(t, 48, stepEquivalentCount(360, 10, 75), 0.001)

	// A provider whose period IS the step needs no conversion.
	assert.InDelta(t, 50, stepEquivalentCount(50, 75, 75), 0.001)
}

// Converting on a number we did not measure would be a guess dressed as coverage.
func TestStepEquivalentCount_UnmeasurableIsLeftAlone(t *testing.T) {
	assert.Equal(t, float64(9), stepEquivalentCount(9, 0, 75), "no sample period")
	assert.Equal(t, float64(9), stepEquivalentCount(9, 10, 0), "no step")
	assert.Equal(t, float64(9), stepEquivalentCount(9, math.Inf(1), 75))
	// NaN fails every comparison, so it has to be excluded by name or it slips past
	// the `<= 0` guards and turns a real count into null.
	assert.Equal(t, float64(9), stepEquivalentCount(9, math.NaN(), 75), "NaN period")
	assert.Equal(t, float64(9), stepEquivalentCount(9, 10, math.NaN()), "NaN step")
	assert.True(t, math.IsNaN(stepEquivalentCount(math.NaN(), 10, 75)), "a NaN count stays NaN, not a number")
	assert.Equal(t, float64(0), stepEquivalentCount(0, 10, 75), "nothing observed stays nothing")
}

func TestAggregateStepSeconds(t *testing.T) {
	assert.Equal(t, float64(75), aggregateStepSeconds(RequestMetadata{Step: "75s"}))
	assert.Equal(t, float64(300), aggregateStepSeconds(RequestMetadata{Step: "5m"}))
	assert.Equal(t, float64(0), aggregateStepSeconds(RequestMetadata{}),
		"a directly constructed metadata carries no step and must not be converted on")
	assert.Equal(t, float64(0), aggregateStepSeconds(RequestMetadata{Step: "14d"}),
		"PromQL day literals are not Go durations; unparseable means do not convert")
}

// The contract's own words: every provider answers count in the same unit. This pins
// the three conversions against one another rather than each against itself.
func TestCountMeansTheSameThingOnEveryProvider(t *testing.T) {
	const step = 75.0
	const hour = 3600.0

	// An hour of observation, however each provider stored it.
	prometheus := hour / step // count_over_time at a 75s step
	datadog := stepEquivalentCount(hour/300, 300, step)
	elasticsearch := stepEquivalentCount(hour/10, 10, step)

	require.InDelta(t, prometheus, datadog, 0.001)
	require.InDelta(t, prometheus, elasticsearch, 0.001)
	assert.InDelta(t, 48, prometheus, 0.001, "an hour is 48 steps, and so under a 50-step floor")
}
