package observability

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The label-values response must grow linearly with the number of values.
// It previously grew quadratically: each of the N items carried a copy of the
// whole N-value payload, so a 2362-value 'pod' lookup marshalled to 193MiB and
// OOM-killed both the API pod and llm-server. 5000 values marshal to ~120KB
// linear; the old shape would exceed 300MB, so the ceiling separates them by
// three orders of magnitude and cannot pass by accident.
func TestLabelValuesOutputIsLinearInValueCount(t *testing.T) {
	const n = 5000
	values := make([]string, n)
	for i := range values {
		values[i] = fmt.Sprintf("some-workload-name-%05d-abcdefghij", i)
	}

	body, err := json.Marshal(labelValuesOutput(values))
	require.NoError(t, err)

	perValue := len(body) / n
	assert.Less(t, perValue, 200,
		"response must stay O(N): %d bytes for %d values is %d bytes/value", len(body), n, perValue)
}

// Attributes carried the entire provider reply, which is what made the response
// quadratic. Nothing downstream reads it, so it must stay empty.
func TestLabelValuesOutputCarriesNoPerValueAttributes(t *testing.T) {
	out := labelValuesOutput([]string{"api", "web"})

	require.Len(t, out, 2)
	assert.Equal(t, []OutputMetricsLabelValues{
		{Value: "api", Attributes: map[string]any{}},
		{Value: "web", Attributes: map[string]any{}},
	}, out)
	for _, v := range out {
		assert.Empty(t, v.Attributes, "per-value attributes must stay empty")
	}
}

func TestLabelValuesOutputHandlesEmptyInput(t *testing.T) {
	assert.Empty(t, labelValuesOutput(nil))
}

// The caller's metric must scope the lookup. It used to be dropped, so every
// label lookup ran across the whole cluster and returned an MB-scale payload
// that the quadratic bug above then amplified.
func TestRequestedMetricNameReadsCallerMetric(t *testing.T) {
	assert.Equal(t, "up", requestedMetricName(FetchMetricsLabelValueRequest{
		Request: map[string]any{"metric_name": "up"},
	}))
	assert.Equal(t, "up", requestedMetricName(FetchMetricsLabelValueRequest{
		Request: map[string]any{"metric_name": "  up  "},
	}), "surrounding whitespace must not produce a bogus selector")
}

// No metric means no selector — the unscoped action path stays the fallback.
func TestRequestedMetricNameEmptyWhenAbsent(t *testing.T) {
	assert.Empty(t, requestedMetricName(FetchMetricsLabelValueRequest{}))
	assert.Empty(t, requestedMetricName(FetchMetricsLabelValueRequest{Request: map[string]any{}}))
	assert.Empty(t, requestedMetricName(FetchMetricsLabelValueRequest{
		Request: map[string]any{"metric_name": ""},
	}))
	assert.Empty(t, requestedMetricName(FetchMetricsLabelValueRequest{
		Request: map[string]any{"metric_name": 42},
	}), "a non-string metric_name must not panic or leak into the selector")
}

func TestStringListFromProxyData(t *testing.T) {
	values, err := stringListFromProxyData(map[string]any{
		"status": "success",
		"data":   []interface{}{"api", "web", 7},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "web"}, values, "non-string entries are skipped, not fatal")

	_, err = stringListFromProxyData(map[string]any{"status": "success"})
	require.Error(t, err)

	// An error envelope arrives with HTTP 200 on some engines, so it must be
	// surfaced as the provider's message rather than "missing data field".
	_, err = stringListFromProxyData(map[string]any{"status": "error", "error": "invalid selector"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selector")

	_, err = stringListFromProxyData(map[string]any{"status": "error"})
	require.Error(t, err)

	// Reading a nil map is safe in Go, so this returns an error, not a panic.
	assert.NotPanics(t, func() { _, _ = stringListFromProxyData(nil) })
	_, err = stringListFromProxyData(nil)
	require.Error(t, err)

	_, err = stringListFromProxyData(map[string]any{"data": "not-a-list"})
	require.Error(t, err)
}

// %q quotes and escapes by itself; escaping the metric beforehand as well
// produced {__name__="foo\\\"bar"} for a metric containing a quote.
func TestMetricNameSelectorEscapesOnce(t *testing.T) {
	assert.Equal(t, `{__name__="up"}`, metricNameSelector("up"))
	assert.Equal(t, `{__name__="foo\"bar"}`, metricNameSelector(`foo"bar`))
	assert.Equal(t, `{__name__="back\\slash"}`, metricNameSelector(`back\slash`))
}
