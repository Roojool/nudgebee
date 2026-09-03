package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/llm/config"
	"nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNBTraceToolV2_Metadata pins the canonical v2 trace tool's identity and input
// contract. The Call round-trip goes through services-server (QueryTraces) and is
// covered by the gated live e2e; the empty-provider behaviour lives in
// executeFetchTraceCanonical.
func TestNBTraceToolV2_Metadata(t *testing.T) {
	tool := NBTraceToolV2{accountId: "acct"}
	assert.Equal(t, ToolTracesExecuteV2, tool.Name())
	assert.Equal(t, "traces_execute_v2", ToolTracesExecuteV2)
	assert.Equal(t, core.NBToolTypeTool, tool.GetType())

	schema := tool.InputSchema()
	assert.Equal(t, []string{"command"}, schema.Required)
	for _, key := range []string{"command", "start_time", "end_time", "range"} {
		_, ok := schema.Properties[key]
		assert.True(t, ok, "input schema must expose %q", key)
	}
}

// startTracesStub spins up an httptest server standing in for api-server's /rpc/traces
// endpoint and points config.Config.ServiceEndpoint at it. captured, when non-nil, receives
// the decoded `input.request` object so a test can assert the wire shape api-server gets.
func startTracesStub(t *testing.T, body string, captured *map[string]any) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rpc/traces", r.URL.Path)
		if captured != nil {
			var payload struct {
				Input struct {
					Request map[string]any `json:"request"`
				} `json:"input"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			*captured = payload.Input.Request
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))

	prev := config.Config.ServiceEndpoint
	config.Config.ServiceEndpoint = srv.URL
	return func() {
		config.Config.ServiceEndpoint = prev
		srv.Close()
	}
}

// traceMetadataFrom extracts the metadata envelope the tool hands to the agent.
func traceMetadataFrom(t *testing.T, data string) (string, string) {
	t.Helper()
	var doc struct {
		Metadata struct {
			Query    string `json:"query"`
			Provider string `json:"provider"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(data), &doc))
	return doc.Metadata.Query, doc.Metadata.Provider
}

// TestNBTraceToolV2_Call_EmptyResult is the traces counterpart of
// TestNBLogToolV2_Call_EmptyResult: a query that matched nothing must still hand the agent the
// query services-server ran and the provider that answered, so a "no traces found" answer can say
// what was asked and where (#37406). Before the fix this branch returned a bare "[]".
func TestNBTraceToolV2_Call_EmptyResult(t *testing.T) {
	const command = `{"where":{"workload_namespace":{"_eq":"nope"}}}`

	t.Run("empty result carries the executed query and provider", func(t *testing.T) {
		cleanup := startTracesStub(t,
			`{"traces":[],"query":"SELECT * FROM otel_traces WHERE ServiceNamespace = 'nope'","provider":"clickhouse"}`, nil)
		defer cleanup()

		tool := NBTraceToolV2{accountId: "acc-1"}
		resp, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{Command: command})
		require.NoError(t, err)
		assert.Equal(t, core.NBToolResponseStatusSuccess, resp.Status)
		assert.Contains(t, resp.Data, `"traces":[]`, "an empty result must not reach the agent as null")

		query, provider := traceMetadataFrom(t, resp.Data)
		assert.Equal(t, "SELECT * FROM otel_traces WHERE ServiceNamespace = 'nope'", query)
		assert.Equal(t, "clickhouse", provider)
	})

	t.Run("the empty-result diagnosis travels with the metadata", func(t *testing.T) {
		cleanup := startTracesStub(t,
			`{"traces":[],"suggestion":"no traces matched: value \"nope\" for label \"workload_namespace\" not found; closest valid value(s): [prod]","query":"{namespace=\"nope\"}","provider":"jaeger"}`, nil)
		defer cleanup()

		tool := NBTraceToolV2{accountId: "acc-1"}
		resp, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{Command: command})
		require.NoError(t, err)
		assert.Equal(t, core.NBToolResponseStatusSuccess, resp.Status)
		assert.Contains(t, resp.Data, "closest valid value(s): [prod]")

		query, provider := traceMetadataFrom(t, resp.Data)
		assert.Equal(t, `{namespace="nope"}`, query)
		assert.Equal(t, "jaeger", provider)
	})

	t.Run("spans present keep the metadata too", func(t *testing.T) {
		cleanup := startTracesStub(t,
			`{"traces":[{"service_name":"checkout","duration_ns":41020000}],"query":"{service=\"checkout\"}","provider":"jaeger"}`, nil)
		defer cleanup()

		tool := NBTraceToolV2{accountId: "acc-1"}
		resp, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{Command: command})
		require.NoError(t, err)
		assert.Equal(t, core.NBToolResponseStatusSuccess, resp.Status)
		assert.Contains(t, resp.Data, "checkout")

		query, provider := traceMetadataFrom(t, resp.Data)
		assert.Equal(t, `{service="checkout"}`, query)
		assert.Equal(t, "jaeger", provider)
	})

	t.Run("a legacy bare-array body still decodes", func(t *testing.T) {
		// Version skew: an older services-server returns the bare span array with no envelope.
		// The tool must keep working; the metadata is simply empty.
		cleanup := startTracesStub(t, `[{"service_name":"checkout"}]`, nil)
		defer cleanup()

		tool := NBTraceToolV2{accountId: "acc-1"}
		resp, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{Command: command})
		require.NoError(t, err)
		assert.Equal(t, core.NBToolResponseStatusSuccess, resp.Status)
		assert.Contains(t, resp.Data, "checkout")
	})
}

// TestNBTraceToolV2_Call_RequestsExecutedQuery pins the flag on the wire: without
// include_executed_query api-server never resolves the query, so every layer downstream would
// carry an empty one no matter what it does with the metadata.
func TestNBTraceToolV2_Call_RequestsExecutedQuery(t *testing.T) {
	var got map[string]any
	cleanup := startTracesStub(t, `{"traces":[],"query":"q","provider":"clickhouse"}`, &got)
	defer cleanup()

	tool := NBTraceToolV2{accountId: "acc-1"}
	_, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{
		Command: `{"where":{"workload_namespace":{"_eq":"prod"}}}`,
	})
	require.NoError(t, err)

	assert.Equal(t, true, got["include_executed_query"], "the executed query must be requested: %v", got)
	assert.Equal(t, true, got["validate_request"], "the empty-result diagnosis must stay requested")
}
