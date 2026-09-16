package eventrule

import (
	"fmt"

	"nudgebee/services/relay"
)

// executeAgentAction runs a rule write on the k8s agent and fails when either
// the relay call or the agent's own result fails.
func executeAgentAction(req relay.RelayExecuteRequest) error {
	resp, err := relay.Execute(req)
	if err != nil {
		return fmt.Errorf("agent %s: %w", req.Body.ActionName, err)
	}
	if err := relay.ActionSucceeded(resp); err != nil {
		return fmt.Errorf("agent %s: %w", req.Body.ActionName, err)
	}
	return nil
}

// mergeRuleMetadata lays the edit form's severity and standard annotations over
// the stored maps, so an edit keeps every label and annotation the form does not
// show (team, runbook_url, ...).
func mergeRuleMetadata(stored toggledRule, req EventConfig) (map[string]string, map[string]string) {
	labels := make(map[string]string, len(stored.Labels)+1)
	for k, v := range stored.Labels {
		labels[k] = v
	}
	if req.Labels.Severity != "" {
		labels["severity"] = req.Labels.Severity
	}

	annotations := make(map[string]string, len(stored.Annotations)+3)
	for k, v := range stored.Annotations {
		annotations[k] = v
	}
	annotations["description"] = req.Annotations.Description
	annotations["summary"] = req.Annotations.Summary
	if req.Annotations.Runbook != "" {
		annotations["runbook"] = req.Annotations.Runbook
	}
	return labels, annotations
}

// toStringMap converts the labels/annotations value UpdateEventRule holds (a
// merged map or the request's typed struct) into the provider-facing map.
func toStringMap(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		return m
	case struct {
		Severity string `json:"severity"`
	}:
		return map[string]string{"severity": m.Severity}
	case struct {
		Description string `json:"description"`
		Summary     string `json:"summary"`
		Runbook     string `json:"runbook"`
	}:
		out := map[string]string{"description": m.Description, "summary": m.Summary}
		if m.Runbook != "" {
			out["runbook"] = m.Runbook
		}
		return out
	}
	return map[string]string{}
}
