package observability

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Datadog fills intervals a series has no data in with [ts, null]. That is the
// normal shape of a per-pod query over a long window: each pod only existed for
// part of it. Decoded into []float64 those nulls became 0, indistinguishable from
// a measured zero — which drags a percentile down, and counts as a sample for any
// caller sizing a workload on how many samples it has.
func TestDatadogMapToOutputMetric_NullPointsAreGapsNotZeroes(t *testing.T) {
	body := `{
		"status": "ok",
		"res_type": "time_series",
		"series": [
			{
				"metric": "kubernetes.cpu.usage.total",
				"tag_set": ["pod_name:api-7d9f", "kube_container_name:api"],
				"pointlist": [[1700000000000, null], [1700000060000, 1500000.0], [1700000120000, null]]
			}
		]
	}`

	out, err := datadogMapToOutputMetricWithKey([]byte(body), "cpu")
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	require.Len(t, out.Results[0].Payload, 1)
	series := out.Results[0].Payload[0]

	// Every point keeps its position, so values and timestamps stay index-aligned.
	assert.Equal(t, []int64{1700000000, 1700000060, 1700000120}, series.Timestamps)
	require.Len(t, series.Values, 3)
	assert.True(t, math.IsNaN(series.Values[0]), "a gap is not a measurement")
	assert.InDelta(t, 1500000.0, series.Values[1], 1e-9)
	assert.True(t, math.IsNaN(series.Values[2]))
	assert.Equal(t, map[string]int{nonFiniteGap: 2}, series.NonFinite)

	// On the wire the gaps are JSON null, which is what a client drops rather than
	// reads as zero. The count says why the holes are there.
	encoded, err := json.Marshal(series)
	require.NoError(t, err)
	var wire struct {
		Values    []*float64     `json:"values"`
		NonFinite map[string]int `json:"non_finite"`
	}
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.Len(t, wire.Values, 3)
	assert.Nil(t, wire.Values[0])
	require.NotNil(t, wire.Values[1])
	assert.InDelta(t, 1500000.0, *wire.Values[1], 1e-9)
	assert.Nil(t, wire.Values[2])
	assert.Equal(t, map[string]int{nonFiniteGap: 2}, wire.NonFinite,
		"gaps are reported as gaps, not folded into the nan tally")
}

// A series of nothing but gaps is still a series: the pod existed, the query was
// right, and there is no data. Reporting it as empty would look like a bad query.
func TestDatadogMapToOutputMetric_AllNullSeries(t *testing.T) {
	body := `{"series":[{"metric":"m","tag_set":["pod_name:p"],"pointlist":[[1700000000000,null]]}]}`

	out, err := datadogMapToOutputMetricWithKey([]byte(body), "k")
	require.NoError(t, err)
	require.Len(t, out.Results[0].Payload, 1)
	series := out.Results[0].Payload[0]
	assert.Equal(t, []int64{1700000000}, series.Timestamps)
	assert.Equal(t, map[string]int{nonFiniteGap: 1}, series.NonFinite)

	encoded, err := json.Marshal(series)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"values":[null]`)
}
