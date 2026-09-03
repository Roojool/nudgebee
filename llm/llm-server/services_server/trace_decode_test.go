package services_server

import (
	"testing"

	core "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecodeTraceQueryBody covers the version-skew-tolerant decode for the free-form ClickHouse
// trace path: the upgraded services-server returns the {result:{...}} object, while an older one
// (or a non-clickhouse fallthrough) returns the bare typed array. Both must decode without error.
func TestDecodeTraceQueryBody(t *testing.T) {
	t.Run("object shape with raw result", func(t *testing.T) {
		body := []byte(`{"result":{"columns":["service","p95_latency_ns"],"column_types":["String","Float64"],"rows":[["notifications",84213000]]}}`)
		var resp core.ObservabilityTraceResponse
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		require.NotNil(t, resp.Result, "raw result must be populated from the object body")
		assert.Equal(t, []string{"service", "p95_latency_ns"}, resp.Result.Columns)
		require.Len(t, resp.Result.Rows, 1)
		assert.EqualValues(t, 84213000, resp.Result.Rows[0][1])
		assert.Empty(t, resp.Traces, "typed array must stay empty on the raw path")
	})

	t.Run("legacy bare array falls back to typed traces", func(t *testing.T) {
		// includeRaw=true but an older services-server returned a bare array (no result object).
		body := []byte(`[{"service_name":"notifications","duration_ns":84213000,"span_name":"GET /notify"}]`)
		var resp core.ObservabilityTraceResponse
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		assert.Nil(t, resp.Result, "no raw result in a bare-array body")
		require.Len(t, resp.Traces, 1)
		assert.Equal(t, "notifications", resp.Traces[0].ServiceName)
		assert.EqualValues(t, 84213000, resp.Traces[0].DurationNs)
	})

	// The validated path: services-server returns 200 with an empty span list and the diagnosis
	// of WHY it is empty. Losing `suggestion` here is the regression this guards.
	t.Run("validation envelope carries the suggestion", func(t *testing.T) {
		body := []byte(`{"traces":[],"suggestion":"no traces matched: value \"services-serve\" for label \"workload_name\" not found; closest valid value(s): [services-server]"}`)
		var resp core.ObservabilityTraceResponse
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		assert.Empty(t, resp.Traces)
		assert.Nil(t, resp.Result)
		assert.Contains(t, resp.Suggestion, "services-server")
	})

	t.Run("envelope with spans keeps them and carries no suggestion", func(t *testing.T) {
		body := []byte(`{"traces":[{"service_name":"checkout","duration_ns":41020000}]}`)
		var resp core.ObservabilityTraceResponse
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		require.Len(t, resp.Traces, 1)
		assert.Equal(t, "checkout", resp.Traces[0].ServiceName)
		assert.Empty(t, resp.Suggestion)
	})

	t.Run("non-raw path decodes typed array", func(t *testing.T) {
		body := []byte(`[{"service_name":"checkout","duration_ns":41020000}]`)
		var resp core.ObservabilityTraceResponse
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		assert.Nil(t, resp.Result)
		require.Len(t, resp.Traces, 1)
		assert.Equal(t, "checkout", resp.Traces[0].ServiceName)
	})

	// The executed query and provider must survive the decode on an empty result, otherwise the
	// agent can only report "no traces found" with nothing to attribute it to (#37406). The
	// canonical path sends both request fields empty, so the wire values are the only real ones.
	t.Run("envelope carries the executed query and provider on an empty result", func(t *testing.T) {
		body := []byte(`{"traces":[],"query":"SELECT * FROM otel_traces WHERE ServiceName = 'checkout'","provider":"clickhouse"}`)
		resp := core.ObservabilityTraceResponse{
			Metadata: core.ObservabilityTraceMetadata{Query: "", Provider: ""},
		}
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		assert.Empty(t, resp.Traces)
		assert.Equal(t, "SELECT * FROM otel_traces WHERE ServiceName = 'checkout'", resp.Metadata.Query)
		assert.Equal(t, "clickhouse", resp.Metadata.Provider)
	})

	t.Run("envelope with spans also carries the executed query and provider", func(t *testing.T) {
		body := []byte(`{"traces":[{"service_name":"checkout"}],"query":"{service=\"checkout\"}","provider":"jaeger"}`)
		var resp core.ObservabilityTraceResponse
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		require.Len(t, resp.Traces, 1)
		assert.Equal(t, `{service="checkout"}`, resp.Metadata.Query)
		assert.Equal(t, "jaeger", resp.Metadata.Provider)
	})

	t.Run("a body without query or provider keeps the request-seeded metadata", func(t *testing.T) {
		// Version skew: an older services-server omits both fields, so the values QueryTraces
		// seeded from the request (the requested provider) must not be blanked out.
		body := []byte(`{"traces":[],"suggestion":"no traces matched"}`)
		resp := core.ObservabilityTraceResponse{
			Metadata: core.ObservabilityTraceMetadata{Query: "seeded-query", Provider: "jaeger"},
		}
		require.NoError(t, decodeTraceQueryBody(body, &resp))

		assert.Equal(t, "seeded-query", resp.Metadata.Query)
		assert.Equal(t, "jaeger", resp.Metadata.Provider)
	})
}
