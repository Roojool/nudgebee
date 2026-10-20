package relay

import (
	"strings"
	"testing"
)

func TestAgentProbeReplyError(t *testing.T) {
	cases := []struct {
		name    string
		probe   agentProbe
		resp    map[string]any
		wantErr string // substring; empty means success
	}{
		{
			name:  "Loki answered",
			probe: agentLokiProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": map[string]any{"status": "success", "data": []any{"namespace"}}}},
		},
		{
			name:    "Loki not configured on the Go agent",
			probe:   agentLokiProbe,
			resp:    map[string]any{"status_code": 404, "data": map[string]any{"error": "action not registered: query_loki_labels"}},
			wantErr: "set runner.loki.url",
		},
		{
			// What the agent really sends when Loki answers 401: rawGet passes the
			// body through and wrapData still says success.
			name:  "Loki rejects the request with a JSON error",
			probe: agentLokiProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": map[string]any{"status": "error", "message": "authentication required"}}},
			wantErr: "reached its Loki but the query failed: authentication required",
		},
		{
			name:  "Loki answers a non-JSON error page",
			probe: agentLokiProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": "404 page not found\n"}},
			wantErr: "the query failed: 404 page not found — check runner.loki.url",
		},
		{
			name:  "Loki returns an empty body",
			probe: agentLokiProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": nil}},
			wantErr: "Loki returned an empty response",
		},
		{
			// A transport failure (DNS, dial, TLS) is the one case wrapData reports
			// as success:false.
			name:  "Loki unreachable from the agent",
			probe: agentLokiProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": false, "data": map[string]any{"error": "dial tcp 10.0.0.9:3100: connect: connection refused"}}},
			wantErr: "could not query its Loki: dial tcp 10.0.0.9:3100: connect: connection refused — check runner.loki.url",
		},
		{
			name:  "Elasticsearch answered with its index list",
			probe: agentElasticsearchProbe,
			resp: map[string]any{"status_code": 200, "data": []any{
				map[string]any{"index": "logs-2026.09.25", "health": "green"}}},
		},
		{
			name:  "Elasticsearch disabled on the agent",
			probe: agentElasticsearchProbe,
			resp: map[string]any{"status_code": 500, "data": map[string]any{
				"error": `elasticsearch datasource not configured on this agent (action "query_es_indices")`}},
			wantErr: "runner.es.enabled=true",
		},
		{
			name:  "Elasticsearch error keeps its message",
			probe: agentElasticsearchProbe,
			resp: map[string]any{"status_code": 500, "data": map[string]any{
				"error": "elasticsearch: 403 security_exception"}},
			wantErr: "403 security_exception",
		},
		{
			name:  "ClickHouse answered",
			probe: agentClickHouseProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": map[string]any{"data": []any{[]any{1}}, "error": nil}}},
		},
		{
			name:  "ClickHouse off: success envelope carrying not configured",
			probe: agentClickHouseProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": map[string]any{"data": nil, "error": "clickhouse: not configured"}}},
			wantErr: "opentelemetry-collector.enabled=true",
		},
		{
			name:  "ClickHouse query error",
			probe: agentClickHouseProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": map[string]any{"data": nil, "error": "Code: 516. Authentication failed"}}},
			wantErr: "reached its ClickHouse but the query failed: Code: 516",
		},
		{
			name:  "ClickHouse hard error",
			probe: agentClickHouseProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": false, "data": nil, "msg": "dial tcp: connection refused"}},
			wantErr: "could not query its ClickHouse: dial tcp: connection refused",
		},
		{
			name:  "ClickHouse unreadable result",
			probe: agentClickHouseProbe,
			resp: map[string]any{"status_code": 200, "data": map[string]any{
				"success": true, "data": "nope"}},
			wantErr: "unreadable reply",
		},
		{
			name:    "empty reply",
			probe:   agentLokiProbe,
			resp:    map[string]any{"status_code": 200},
			wantErr: "empty reply to the Loki probe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := agentProbeReplyError(tc.resp, tc.probe)
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

func TestRedactCredentials(t *testing.T) {
	cases := []struct {
		in       string
		leaks    []string
		keepsAll []string
	}{
		{
			// The agent's ClickHouse client puts the password in the query string,
			// and Go's HTTP client quotes the whole URL in transport errors.
			in:       `Post "http://clickhouse:8123/?database=otel&password=s3cr3t&user=default": dial tcp: connection refused`,
			leaks:    []string{"s3cr3t"},
			keepsAll: []string{"password=REDACTED", "user=default", "connection refused"},
		},
		{
			in:       `Get "https://admin:hunter2@loki.example:3100/loki/api/v1/labels": x509: certificate signed by unknown authority`,
			leaks:    []string{"hunter2", "admin:"},
			keepsAll: []string{"https://REDACTED@loki.example:3100", "x509"},
		},
		{
			in:       `request failed with Authorization: Bearer eyJhbGciOi.abc-DEF_123 and api_key=AKIA1234`,
			leaks:    []string{"eyJhbGciOi", "AKIA1234"},
			keepsAll: []string{"Bearer REDACTED", "api_key=REDACTED"},
		},
		{
			in:       `Authorization: ApiKey dGVzdDpzZWNyZXRhcGlrZXkxMjM0`,
			leaks:    []string{"dGVzdDpzZWNyZXRhcGlrZXkxMjM0"},
			keepsAll: []string{"ApiKey REDACTED"},
		},
		{
			in:       `Authorization: Api-Key abc123secret, retried with ?api-key=xyz789secret`,
			leaks:    []string{"abc123secret", "xyz789secret"},
			keepsAll: []string{"Api-Key REDACTED", "api-key=REDACTED"},
		},
		{
			in:       "elasticsearch: 403 security_exception",
			keepsAll: []string{"elasticsearch: 403 security_exception"},
		},
	}
	for _, tc := range cases {
		got := redactCredentials(tc.in)
		for _, secret := range tc.leaks {
			if strings.Contains(got, secret) {
				t.Errorf("redactCredentials(%q) = %q still contains %q", tc.in, got, secret)
			}
		}
		for _, want := range tc.keepsAll {
			if !strings.Contains(got, want) {
				t.Errorf("redactCredentials(%q) = %q, want it to contain %q", tc.in, got, want)
			}
		}
	}
}
