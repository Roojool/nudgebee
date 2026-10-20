package relay

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// agentProbeTimeout bounds every probe well under the relay's own wait for the
// agent (180s), which is too long for someone waiting on a button.
const agentProbeTimeout = 30 * time.Second

// agentProbe describes one "can the agent reach its <datasource>" check. The
// transport, agent-envelope and redaction handling is shared; what differs per
// datasource is the action, how "not configured" is spelled, and what a reply
// that got through means.
type agentProbe struct {
	datasource string // user-facing name, e.g. "Loki"
	action     string
	params     map[string]any
	// notConfigured is the cause and fix returned when the agent has no such
	// datasource — it either never registered the action (404) or answers with
	// the "<ds> datasource not configured on this agent" stub.
	notConfigured string
	// fixHint is appended when the agent reached the datasource and it failed.
	fixHint string
	// check interprets a reply that carried no agent-level error. It returns the
	// datasource's own failure message ("" when the probe answered), whether the
	// reply says the datasource is not configured, or an error for a reply it
	// cannot read.
	check func(data any) (failure string, notConfigured bool, err error)
}

// runAgentProbe executes p through the relay and returns nil when the agent's
// datasource answered, or an error naming the cause. Every message is passed
// through redactCredentials: agent errors quote datasource URLs verbatim, and the
// agent's ClickHouse client carries its password in the query string.
func runAgentProbe(accountID string, p agentProbe) error {
	if err := runAgentProbeUnredacted(accountID, p); err != nil {
		return errors.New(redactCredentials(err.Error()))
	}
	return nil
}

func runAgentProbeUnredacted(accountID string, p agentProbe) error {
	start := time.Now()
	resp, err := Execute(RelayExecuteRequest{
		Body: ActionExecuteBody{
			AccountID:    accountID,
			ActionName:   p.action,
			ActionParams: p.params,
			Origin:       "services-server",
		},
		NoSinks:        true,
		Cache:          false,
		TimeoutSeconds: int(agentProbeTimeout.Seconds()),
	})
	if err != nil {
		if err.Error() == errMsgAgentNotConnected {
			return fmt.Errorf("the Nudgebee agent for this cluster is not connected, so its %s "+
				"cannot be reached — check that the nudgebee-agent pods are running and can reach Nudgebee", p.datasource)
		}
		// Execute reports a client timeout as an opaque transport error. Reaching the
		// deadline means the relay accepted the request but the agent never answered —
		// seen on dev for agent rows still marked connected weeks after going silent.
		if time.Since(start) >= agentProbeTimeout {
			return fmt.Errorf("the Nudgebee agent for this cluster did not answer within %s — it is registered "+
				"but not responding; check the nudgebee-agent runner pod's status and logs", agentProbeTimeout)
		}
		return fmt.Errorf("could not reach the Nudgebee agent for this cluster: %w", err)
	}
	return agentProbeReplyError(resp, p)
}

// agentProbeReplyError reads an agent reply and returns the cause of failure, or
// nil when the probe answered. Split out to be testable without a relay.
//
// Two agent generations answer with different envelopes (see ebpf_flow_source.go):
// the Go k8s-agent sends {"status_code": N, "data": {"error": "..."}} on failure,
// the legacy Python agent {"data": {"success": false, "msg": "..."}}. Anything
// else is the action's own reply, handed to p.check.
func agentProbeReplyError(resp map[string]any, p agentProbe) error {
	raw, ok := resp["data"]
	if !ok || raw == nil {
		return fmt.Errorf("the Nudgebee agent returned an empty reply to the %s probe", p.datasource)
	}

	if data, ok := raw.(map[string]any); ok {
		if agentErr, ok := data["error"].(string); ok && agentErr != "" {
			if strings.HasPrefix(agentErr, "action not registered") || strings.Contains(agentErr, "not configured on this agent") {
				return errors.New(p.notConfigured)
			}
			return fmt.Errorf("the Nudgebee agent could not run the %s probe (status %v): %s",
				p.datasource, resp["status_code"], agentErr)
		}
		if success, ok := data["success"].(bool); ok && !success {
			return fmt.Errorf("the Nudgebee agent could not query its %s: %s%s", p.datasource, replyMessage(data), p.fixHint)
		}
	}

	failure, notConfigured, err := p.check(raw)
	if notConfigured {
		return errors.New(p.notConfigured)
	}
	if err != nil {
		return fmt.Errorf("the Nudgebee agent returned an unreadable reply to the %s probe: %w", p.datasource, err)
	}
	if failure != "" {
		return fmt.Errorf("the Nudgebee agent reached its %s but the query failed: %s%s", p.datasource, failure, p.fixHint)
	}
	return nil
}

// replyMessage returns the reason a {"success": false} reply carries: "msg" on the
// legacy agent and query_data, data.error on the Go agent's Loki handlers.
func replyMessage(data map[string]any) string {
	if msg, _ := data["msg"].(string); msg != "" {
		return msg
	}
	if inner, ok := data["data"].(map[string]any); ok {
		if msg, _ := inner["error"].(string); msg != "" {
			return msg
		}
	}
	return "no reason given"
}

var (
	// scheme://user:pass@host → scheme://REDACTED@host
	urlUserinfoPattern = regexp.MustCompile(`(://)[^/\s:@"']+:[^/\s@"']+@`)
	// password=..., token=..., api_key=... in query strings and key=value text
	secretParamPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|access_token|api[_-]?key|secret)=[^&\s"']+`)
	// Authorization: Bearer ... / Basic ... / ApiKey ... (the agent's ES client)
	authHeaderPattern = regexp.MustCompile(`(?i)\b(bearer|basic|api[_-]?key)\s+[A-Za-z0-9._~+/=-]+`)
)

// redactCredentials strips credentials an agent error may quote back: Go's HTTP
// client includes the full request URL in transport errors.
func redactCredentials(msg string) string {
	msg = urlUserinfoPattern.ReplaceAllString(msg, "${1}REDACTED@")
	msg = secretParamPattern.ReplaceAllString(msg, "${1}=REDACTED")
	return authHeaderPattern.ReplaceAllString(msg, "${1} REDACTED")
}

// TestAgentLoki checks that the in-cluster agent can reach its Loki. Listing
// label names needs no stream to exist, so it reports on reachability and auth.
// It calls the same compat action the log queries use, so it reports what those
// queries will get.
func TestAgentLoki(accountID string) error {
	return runAgentProbe(accountID, agentLokiProbe)
}

var agentLokiProbe = agentProbe{
	datasource: "Loki",
	action:     "query_loki_labels",
	params:     map[string]any{},
	notConfigured: "the Nudgebee agent is connected but has no Loki configured, and none was " +
		"auto-discovered — set runner.loki.url in the agent's Helm values and upgrade the release",
	fixHint: " — check runner.loki.url (and runner.loki.username / password or headers, if it needs auth) in the agent's Helm values",
	// The agent's compat handler answers {"success": true, "data": <body>} even when
	// Loki itself answered 4xx/5xx — it passes the body through rather than failing
	// (k8s-agent enrichers/loki.go rawGet) — so success only means the request was
	// made. A healthy labels call is {"status": "success", ...}; anything else is
	// Loki's error, JSON or plain text.
	check: func(raw any) (string, bool, error) {
		data, _ := raw.(map[string]any)
		switch body := data["data"].(type) {
		case map[string]any:
			if body["status"] == "success" {
				return "", false, nil
			}
			if msg, _ := body["message"].(string); msg != "" {
				return msg, false, nil
			}
			if msg, _ := body["error"].(string); msg != "" {
				return msg, false, nil
			}
			return fmt.Sprintf("Loki answered with status %v", body["status"]), false, nil
		case string:
			return strings.TrimSpace(body), false, nil
		case nil:
			return "Loki returned an empty response", false, nil
		default:
			return "", false, fmt.Errorf("unexpected Loki response of type %T", body)
		}
	},
}

// TestAgentElasticsearch checks that the in-cluster agent can reach its
// Elasticsearch by listing indices.
func TestAgentElasticsearch(accountID string) error {
	return runAgentProbe(accountID, agentElasticsearchProbe)
}

var agentElasticsearchProbe = agentProbe{
	datasource: "Elasticsearch",
	action:     "query_es_indices",
	params:     map[string]any{},
	notConfigured: "the Nudgebee agent is connected but has no Elasticsearch configured — set " +
		"runner.es.enabled=true and runner.es.url in the agent's Helm values and upgrade the release",
	fixHint: " — check runner.es.url (and runner.es.apiKey or headers, if it needs auth) in the agent's Helm values",
	// A reachable cluster answers with the _cat/indices array; the agent reports
	// ES failures as a Go-agent error envelope, handled before check runs.
	check: func(any) (string, bool, error) { return "", false, nil },
}

// TestAgentClickHouse checks that the in-cluster agent can query the ClickHouse
// that holds its OpenTelemetry traces, with `SELECT 1`.
func TestAgentClickHouse(accountID string) error {
	return runAgentProbe(accountID, agentClickHouseProbe)
}

var agentClickHouseProbe = agentProbe{
	datasource: "ClickHouse",
	action:     "query_data",
	params:     map[string]any{"query": "SELECT 1"},
	notConfigured: "the Nudgebee agent is connected but has no ClickHouse configured — enable the " +
		"in-chart one with opentelemetry-collector.enabled=true, or set runner.clickhouse_enabled=true " +
		"with a CLICKHOUSE_HOST, in the agent's Helm values",
	fixHint: " — check the runner's ClickHouse host and credentials in the agent's Helm values",
	check: func(raw any) (string, bool, error) {
		// query_data answers {"success": true, "data": {"data": [[...]], "error": null}}
		// even when ClickHouse is off: the agent then sets error to
		// "clickhouse: not configured" instead of failing the action.
		data, _ := raw.(map[string]any)
		result, ok := data["data"].(map[string]any)
		if !ok {
			return "", false, errors.New("query_data result is not an object")
		}
		if msg, _ := result["error"].(string); msg != "" {
			return msg, strings.Contains(msg, "not configured"), nil
		}
		if rows, _ := result["data"].([]any); len(rows) == 0 {
			return "SELECT 1 returned no rows", false, nil
		}
		return "", false, nil
	},
}
