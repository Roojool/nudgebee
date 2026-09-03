package tools

import (
	"testing"

	"nudgebee/llm/common"
	core "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An empty trace result must carry both the diagnosis and the query/provider that produced it,
// rather than a bare "[]" the agent can only guess at — guessing is what drives it to re-issue
// near-identical queries, and what leaves a "no traces found" answer unattributable (#37406).
func TestTracePayloadForResponse(t *testing.T) {
	metadata := core.ObservabilityTraceMetadata{
		StartTime: 1700000000000,
		EndTime:   1700003600000,
		Limit:     100,
		Query:     `SELECT * FROM otel_traces WHERE ServiceName = 'checkout'`,
		Provider:  "clickhouse",
	}

	t.Run("empty with a suggestion keeps the suggestion and the metadata", func(t *testing.T) {
		resp := core.ObservabilityTraceResponse{
			Traces:     []core.ObservabilityTrace{},
			Metadata:   metadata,
			Suggestion: `no traces matched: unknown label name(s) [namespace] for this trace provider; closest valid label(s): [workload_namespace]`,
		}
		payload := tracePayloadForResponse(resp)
		assert.Contains(t, payload.Suggestion, "workload_namespace")
		assert.Equal(t, metadata.Query, payload.Metadata.Query)
		assert.Equal(t, "clickhouse", payload.Metadata.Provider)
	})

	t.Run("empty without a suggestion still carries the query and provider", func(t *testing.T) {
		// The case the ticket exists for: the most common "no traces found" is a clean empty
		// result, which used to reach the agent as "[]" with nothing else.
		resp := core.ObservabilityTraceResponse{Traces: []core.ObservabilityTrace{}, Metadata: metadata}
		payload := tracePayloadForResponse(resp)
		assert.Empty(t, payload.Traces)
		assert.Equal(t, metadata.Query, payload.Metadata.Query)
		assert.Equal(t, "clickhouse", payload.Metadata.Provider)

		encoded, err := common.MarshalJson(payload)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"traces":[]`)
		assert.Contains(t, string(encoded), `"provider":"clickhouse"`)
		assert.NotContains(t, string(encoded), `"suggestion"`, "an absent suggestion stays omitted")
	})

	t.Run("nil traces marshal as [] rather than null", func(t *testing.T) {
		// A provider returning a nil slice must not reach the agent as "null" — that reads as a
		// broken tool rather than a window with no spans in it.
		resp := core.ObservabilityTraceResponse{Traces: nil, Metadata: metadata}
		payload := tracePayloadForResponse(resp)
		assert.NotNil(t, payload.Traces, "nil would serialize to null")

		encoded, err := common.MarshalJson(payload)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"traces":[]`)
		assert.NotContains(t, string(encoded), `"traces":null`)
	})

	t.Run("spans present keep the spans and gain the metadata", func(t *testing.T) {
		resp := core.ObservabilityTraceResponse{
			Traces:   []core.ObservabilityTrace{{ServiceName: "checkout"}},
			Metadata: metadata,
		}
		payload := tracePayloadForResponse(resp)
		require.Len(t, payload.Traces, 1)
		assert.Equal(t, "checkout", payload.Traces[0].ServiceName)
		assert.Equal(t, "clickhouse", payload.Metadata.Provider)

		// The deliberate shape change: a successful result is now an envelope, not a bare array,
		// so the query that produced the spans travels with them too.
		encoded, err := common.MarshalJson(payload)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"metadata"`)
		assert.Contains(t, string(encoded), `"checkout"`)
	})

	t.Run("the raw ClickHouse table is left alone", func(t *testing.T) {
		// IncludeRawResult bodies keep their own shape; only the nil-slice normalization applies.
		resp := core.ObservabilityTraceResponse{
			Result:   &core.ObservabilityTraceRawTable{Columns: []string{"count()"}},
			Metadata: metadata,
		}
		payload := tracePayloadForResponse(resp)
		require.NotNil(t, payload.Result)
		assert.Equal(t, []string{"count()"}, payload.Result.Columns)
	})
}
