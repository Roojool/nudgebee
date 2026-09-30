package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/llm/config"
	"nudgebee/llm/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsMCPToolCallError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"success result", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`, false},
		{"isError false", `{"result":{"content":[],"isError":false}}`, false},
		{"isError true", `{"result":{"content":[{"type":"text","text":"GET http://x/debug/pprof/heap: connect: connection refused"}],"isError":true}}`, true},
		{"json-rpc error object", `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Unknown tool"}}`, true},
		{"null error", `{"jsonrpc":"2.0","id":1,"result":{"content":[]},"error":null}`, false},
		{"sse framed isError", "event: message\ndata: {\"result\":{\"content\":[],\"isError\":true}}\n\n", true},
		{"sse framed success", "event: message\ndata: {\"result\":{\"content\":[]}}\n\n", false},
		{"not json", `plain text answer`, false},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isMCPToolCallError(tc.in))
		})
	}
}

// Call must report a tool-level MCP failure as an error status (so the stored
// tool-call status and tool-health metrics see it) while returning a nil error
// (so the per-integration circuit breaker does not trip on it).
func TestMCPIntegrationToolCall_Status(t *testing.T) {
	cases := []struct {
		name       string
		mcpReply   string
		wantStatus NBToolResponseStatus
	}{
		{"success", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"heap ok"}]}}`, NBToolResponseStatusSuccess},
		{"isError", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"connect: connection refused"}],"isError":true}}`, NBToolResponseStatusError},
		{"json-rpc error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Unknown tool"}}`, NBToolResponseStatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": tc.mcpReply})
			}))
			defer srv.Close()

			orig := config.Config.RelayServerEndpoint
			config.Config.RelayServerEndpoint = srv.URL
			defer func() { config.Config.RelayServerEndpoint = orig }()

			tool := mcpIntegrationTool{
				config:      mcpIntegrationConfig{IntegrationID: "int-1", ConnectionMode: "vm_agent"},
				toolName:    "pprof_debug_pprof_top",
				mcpToolName: "debug_pprof_top",
			}
			ctx := NbToolContext{AccountId: "acct-1", Ctx: security.NewRequestContext(t.Context(), nil, nil, nil, nil)}

			resp, err := tool.Call(ctx, NBToolCallRequest{Arguments: map[string]any{}})
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, resp.Status)
			assert.Equal(t, tc.mcpReply, resp.Data, "the MCP reply must reach the model unchanged")
			assert.False(t, tool.IsInfrastructureFailure(err, resp), "a tool-level error must not trip the circuit breaker")
		})
	}
}
