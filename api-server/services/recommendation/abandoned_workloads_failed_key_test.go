package recommendation

import (
	"testing"

	"nudgebee/services/observability"

	"github.com/stretchr/testify/assert"
)

// A failed metrics query is not a quiet workload.
//
// computeWorkloadNetworkRate decides whether a workload looks abandoned. Its input is a
// payload that is empty both when the workload genuinely sent no traffic AND when the
// query failed — so summing a failure as zero would mark a live workload abandoned on
// the strength of an error nobody read.

func errp(s string) *string { return &s }

func TestComputeWorkloadNetworkRate_RefusesWhenAKeyFailed(t *testing.T) {
	out := observability.OutputMetricQuery{Results: []observability.QueryResult{
		{QueryKey: "network_receive_packet", Payload: []observability.Result{
			{Values: []float64{500, 600}, Timestamps: []int64{1700000000, 1700086400}},
		}},
		{QueryKey: "network_transmit_packets", Error: errp("datadog rate-limited (429)")},
	}}
	_, _, ok := computeWorkloadNetworkRate(out)
	assert.False(t, ok, "a failed key must stop the calculation, not count as zero traffic")
}

// The same shape with both keys answering must still compute, or the guard has simply
// broken the feature.
func TestComputeWorkloadNetworkRate_ComputesWhenEveryKeyAnswered(t *testing.T) {
	out := observability.OutputMetricQuery{Results: []observability.QueryResult{
		{QueryKey: "network_receive_packet", Payload: []observability.Result{
			{Values: []float64{500, 600}, Timestamps: []int64{1700000000, 1700086400}},
		}},
		{QueryKey: "network_transmit_packets", Payload: []observability.Result{
			{Values: []float64{100, 200}, Timestamps: []int64{1700000000, 1700086400}},
		}},
	}}
	rate, days, ok := computeWorkloadNetworkRate(out)
	assert.True(t, ok)
	assert.Greater(t, rate, 0.0)
	assert.GreaterOrEqual(t, days, 1)
}

// An empty payload with no error is a genuine "no traffic" answer and must stay
// distinguishable from the failure above.
func TestComputeWorkloadNetworkRate_EmptyWithoutErrorIsNotAFailure(t *testing.T) {
	out := observability.OutputMetricQuery{Results: []observability.QueryResult{
		{QueryKey: "network_receive_packet"},
	}}
	_, _, ok := computeWorkloadNetworkRate(out)
	assert.False(t, ok, "no data points at all also cannot produce a rate")
}
