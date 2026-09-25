package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

// probeReply builds a successful-envelope prometheus_queries_enricher reply
// whose probe key holds result, in the nesting both agent generations send.
func probeReply(t *testing.T, result any) map[string]any {
	t.Helper()
	inner, err := json.Marshal(map[string]any{agentPrometheusProbeKey: result})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := json.Marshal([]map[string]any{{"data": string(inner)}})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"status_code": 200,
		"data": map[string]any{
			"findings": []any{map[string]any{
				"evidence": []any{map[string]any{"data": string(blocks)}},
			}},
		},
	}
}

func TestAgentPrometheusProbeError(t *testing.T) {
	cases := []struct {
		name    string
		resp    map[string]any
		wantErr string // substring; empty means success
	}{
		{
			name: "query answered",
			resp: probeReply(t, []any{map[string]any{"metric": map[string]any{}, "value": []any{1.7e9, "1"}}}),
		},
		{
			name: "agent has no Prometheus handler",
			resp: map[string]any{"status_code": 404, "data": map[string]any{
				"error": "action not registered: prometheus_queries_enricher"}},
			wantErr: "globalConfig.prometheus_url",
		},
		{
			name: "Go agent handler error keeps the agent's message",
			resp: map[string]any{"status_code": 504, "data": map[string]any{
				"error": "task timed out"}},
			wantErr: "task timed out",
		},
		{
			name:    "legacy agent reports failure",
			resp:    map[string]any{"data": map[string]any{"success": false, "msg": "prometheus not found"}},
			wantErr: "prometheus not found",
		},
		{
			name: "Prometheus unreachable from the agent",
			resp: probeReply(t, map[string]any{
				"result_type":   "error",
				"string_result": `Post "http://prometheus:9090/api/v1/query": dial tcp: connection refused`,
			}),
			wantErr: "connection refused",
		},
		{
			name:    "empty reply",
			resp:    map[string]any{},
			wantErr: "empty reply",
		},
		{
			name:    "reply without findings",
			resp:    map[string]any{"status_code": 200, "data": map[string]any{}},
			wantErr: "unreadable reply",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := agentPrometheusProbeError(tc.resp)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
