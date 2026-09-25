package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// agentPrometheusProbeKey is the key the probe query is sent and read back under.
const agentPrometheusProbeKey = "probe"

// agentPrometheusProbeTimeout bounds the probe well under the relay's own wait for
// the agent (180s), which is too long for someone waiting on a button.
const agentPrometheusProbeTimeout = 30 * time.Second

// TestAgentPrometheus checks that the in-cluster agent can query its Prometheus,
// and when it cannot, returns an error naming the cause. It runs `vector(1)`,
// which needs no metric to exist, so it reports on reachability rather than on
// whether the cluster has data yet.
//
// It goes to the agent directly rather than through ExecutePrometheus: that path
// refuses accounts whose default metrics provider is a user integration (the
// agent Prometheus is exactly what is being tested here), and it collapses every
// agent failure into one opaque message.
func TestAgentPrometheus(accountID string) error {
	now := time.Now()
	resp, err := Execute(RelayExecuteRequest{
		Body: ActionExecuteBody{
			AccountID:  accountID,
			ActionName: "prometheus_queries_enricher",
			ActionParams: prometheusActionParams(now.Add(-time.Minute), now,
				map[string]string{agentPrometheusProbeKey: "vector(1)"}, true, 0),
			Origin: "services-server",
		},
		NoSinks:        true,
		Cache:          false,
		TimeoutSeconds: int(agentPrometheusProbeTimeout.Seconds()),
	})
	if err != nil {
		if err.Error() == errMsgAgentNotConnected {
			return errors.New("the Nudgebee agent for this cluster is not connected, so its Prometheus " +
				"cannot be reached — check that the nudgebee-agent pods are running and can reach Nudgebee")
		}
		// Execute reports a client timeout as an opaque transport error. Reaching the
		// deadline means the relay accepted the request but the agent never answered —
		// seen on dev for agent rows still marked connected weeks after going silent.
		if time.Since(now) >= agentPrometheusProbeTimeout {
			return fmt.Errorf("the Nudgebee agent for this cluster did not answer within %s — it is registered "+
				"but not responding; check the nudgebee-agent runner pod's status and logs", agentPrometheusProbeTimeout)
		}
		return fmt.Errorf("could not reach the Nudgebee agent for this cluster: %w", err)
	}
	return agentPrometheusProbeError(resp)
}

// agentPrometheusProbeError reads a prometheus_queries_enricher reply and returns
// the cause of failure, or nil when the probe query answered. Split out from
// TestAgentPrometheus to be testable without a relay.
//
// Two agent generations answer with different envelopes (see ebpf_flow_source.go):
// the Go k8s-agent sends {"status_code": N, "data": {"error": "..."}} on failure,
// the legacy Python agent {"data": {"success": false, "msg": "..."}}. On success
// both carry findings[0].evidence[0].data, a JSON string holding [{"data": "<JSON
// string of {key: result}>"}], where a failed query's result is
// {"result_type": "error", "string_result": "<Prometheus error>"}.
func agentPrometheusProbeError(resp map[string]any) error {
	data, ok := resp["data"].(map[string]any)
	if !ok {
		return errors.New("the Nudgebee agent returned an empty reply to the Prometheus probe")
	}

	if agentErr, ok := data["error"].(string); ok && agentErr != "" {
		if strings.HasPrefix(agentErr, "action not registered") {
			return errors.New("the Nudgebee agent is connected but has no Prometheus configured, and none " +
				"was auto-discovered — set globalConfig.prometheus_url in the agent's Helm values and upgrade the release")
		}
		return fmt.Errorf("the Nudgebee agent could not run the Prometheus probe (status %v): %s",
			resp["status_code"], agentErr)
	}
	if success, ok := data["success"].(bool); ok && !success {
		msg, _ := data["msg"].(string)
		if msg == "" {
			msg = "no reason given"
		}
		return fmt.Errorf("the Nudgebee agent could not run the Prometheus probe: %s", msg)
	}

	result, err := agentPrometheusProbeResult(data)
	if err != nil {
		return fmt.Errorf("the Nudgebee agent returned an unreadable reply to the Prometheus probe: %w", err)
	}
	if envelope, ok := result.(map[string]any); ok && envelope["result_type"] == "error" {
		promErr, _ := envelope["string_result"].(string)
		return fmt.Errorf("the Nudgebee agent reached its Prometheus but the query failed: %s — "+
			"check globalConfig.prometheus_url (and prometheus_headers, if it needs auth) in the agent's Helm values", promErr)
	}
	return nil
}

// agentPrometheusProbeResult unwraps the probe key's result from a successful reply.
func agentPrometheusProbeResult(data map[string]any) (any, error) {
	findings, _ := data["findings"].([]any)
	if len(findings) == 0 {
		return nil, errors.New("no findings")
	}
	finding, _ := findings[0].(map[string]any)
	evidence, _ := finding["evidence"].([]any)
	if len(evidence) == 0 {
		return nil, errors.New("no evidence")
	}
	block, _ := evidence[0].(map[string]any)
	blockData, ok := block["data"].(string)
	if !ok {
		return nil, errors.New("evidence data is not a string")
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(blockData), &blocks); err != nil || len(blocks) == 0 {
		return nil, errors.New("evidence data is not a non-empty array")
	}
	inner, ok := blocks[0]["data"].(string)
	if !ok {
		return nil, errors.New("block data is not a string")
	}
	var results map[string]any
	if err := json.Unmarshal([]byte(inner), &results); err != nil {
		return nil, fmt.Errorf("block data is not an object: %w", err)
	}
	result, ok := results[agentPrometheusProbeKey]
	if !ok {
		return nil, errors.New("probe result missing")
	}
	return result, nil
}
