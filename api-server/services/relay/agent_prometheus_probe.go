package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// agentPrometheusProbeKey is the key the probe query is sent and read back under.
const agentPrometheusProbeKey = "probe"

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
	return runAgentProbe(accountID, agentPrometheusProbe(time.Now()))
}

// agentPrometheusProbe is the prometheus_queries_enricher probe. On success the
// reply carries findings[0].evidence[0].data, a JSON string holding [{"data":
// "<JSON string of {key: result}>"}], where a failed query's result is
// {"result_type": "error", "string_result": "<Prometheus error>"}.
func agentPrometheusProbe(now time.Time) agentProbe {
	return agentProbe{
		datasource: "Prometheus",
		action:     "prometheus_queries_enricher",
		params: prometheusActionParams(now.Add(-time.Minute), now,
			map[string]string{agentPrometheusProbeKey: "vector(1)"}, true, 0),
		notConfigured: "the Nudgebee agent is connected but has no Prometheus configured, and none " +
			"was auto-discovered — set globalConfig.prometheus_url in the agent's Helm values and upgrade the release",
		fixHint: " — check globalConfig.prometheus_url (and prometheus_headers, if it needs auth) in the agent's Helm values",
		check: func(raw any) (string, bool, error) {
			data, _ := raw.(map[string]any)
			result, err := agentPrometheusProbeResult(data)
			if err != nil {
				return "", false, err
			}
			if envelope, ok := result.(map[string]any); ok && envelope["result_type"] == "error" {
				promErr, _ := envelope["string_result"].(string)
				return promErr, false, nil
			}
			return "", false, nil
		},
	}
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
