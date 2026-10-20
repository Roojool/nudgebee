package observability

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `aggregate` promises one point per series, on every provider. Datadog pushes a plain
// aggregate down as a rollup only for the per-pod keys; everything else comes back as a
// full range, because its source reads neither `instant` nor `step`. Without the
// collapse here a consumer reading payload[0] gets the OLDEST point in the window
// rather than the statistic it asked for — measured live: cpu_total returned 119 points
// for aggregate=max where Prometheus returned 1.
func TestDatadogCollapseOverTime_CollapsesScalarKeysToo(t *testing.T) {
	for _, tc := range []struct {
		aggregate string
		want      float64
	}{
		{"max", 9}, {"min", 1}, {"sum", 15}, {"avg", 5}, {"count", 3},
	} {
		parsed, err := parseAggregate(tc.aggregate)
		require.NoError(t, err)

		results := []QueryResult{{
			QueryKey: "cpu_total",
			Payload:  []Result{{Timestamps: []int64{1, 2, 3}, Values: []float64{1, 5, 9}}},
		}}
		datadogCollapseOverTime(results, RequestMetadata{Aggregate: parsed})

		require.Len(t, results[0].Payload[0].Values, 1, tc.aggregate)
		assert.InDelta(t, tc.want, results[0].Payload[0].Values[0], 1e-9, tc.aggregate)
		assert.Equal(t, []int64{3}, results[0].Payload[0].Timestamps, tc.aggregate)
	}
}

// A per-pod key with a plain aggregate is reduced HERE, like every other aggregate.
//
// It used to be pushed into the query as `.rollup(max, <window>)` and arrive already
// collapsed, so this path skipped it. That form silently dropped series — 28 of 69 pod
// incarnations on a live 14-day window — so nothing is pushed down any more and this
// is the only place the collapse happens.
func TestDatadogCollapseOverTime_ReducesPerPodKeysHere(t *testing.T) {
	max, err := parseAggregate("max")
	require.NoError(t, err)

	results := []QueryResult{{
		QueryKey: "container_cpu_usage_by_pod",
		Payload:  []Result{{Timestamps: []int64{1, 2, 3}, Values: []float64{1, 5, 9}}},
	}}
	datadogCollapseOverTime(results, RequestMetadata{Aggregate: max})
	require.Len(t, results[0].Payload[0].Values, 1, "an aggregate answers with one point")
	assert.Equal(t, 9.0, results[0].Payload[0].Values[0])
}

// No aggregate means the caller wants the series.
func TestDatadogCollapseOverTime_UntouchedWithoutAnAggregate(t *testing.T) {
	results := []QueryResult{{
		QueryKey: "cpu_total",
		Payload:  []Result{{Timestamps: []int64{1, 2, 3}, Values: []float64{1, 5, 9}}},
	}}
	datadogCollapseOverTime(results, RequestMetadata{})
	assert.Len(t, results[0].Payload[0].Values, 3)
}

// All gaps is not a zero: a scalar key whose window held no reading must say so.
func TestDatadogCollapseOverTime_AllGapsIsNaNNotZero(t *testing.T) {
	max, err := parseAggregate("max")
	require.NoError(t, err)

	results := []QueryResult{{
		QueryKey: "mem_total",
		Payload:  []Result{{Timestamps: []int64{1, 2}, Values: []float64{math.NaN(), math.NaN()}}},
	}}
	datadogCollapseOverTime(results, RequestMetadata{Aggregate: max})
	require.Len(t, results[0].Payload[0].Values, 1)
	assert.True(t, math.IsNaN(results[0].Payload[0].Values[0]))
}
