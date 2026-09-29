package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExecutionTarget is a child-agent selection hint, not an authorization grant.
// Concrete remote tool calls still resolve and authorize their own target.
type ExecutionTarget struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	AccountID string `json:"account_id,omitempty"`
}

// ParseExecutionTarget reads the optional target from an agent tool call.
// The LLM may provide structured Arguments or a raw JSON Command envelope.
func ParseExecutionTarget(input NBToolCallRequest) (*ExecutionTarget, error) {
	raw, found := input.Arguments["target"]
	if !found && input.Command != "" {
		var commandArgs map[string]any
		if json.Unmarshal([]byte(input.Command), &commandArgs) == nil {
			raw, found = commandArgs["target"]
		}
	}
	if !found || raw == nil {
		return nil, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("target must be an object with provider and name")
	}
	rawProvider, found := value["provider"]
	if !found {
		return nil, fmt.Errorf("target is missing the provider field")
	}
	provider, ok := rawProvider.(string)
	if !ok {
		return nil, fmt.Errorf("target provider must be a string")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil, fmt.Errorf("target provider cannot be empty")
	}
	switch provider {
	case "kubernetes", "aws", "gcp", "azure":
	default:
		return nil, fmt.Errorf("target provider must be kubernetes, aws, gcp, or azure")
	}
	rawName, found := value["name"]
	if !found {
		return nil, fmt.Errorf("target is missing the name field")
	}
	name, ok := rawName.(string)
	if !ok {
		return nil, fmt.Errorf("target name must be a string")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("target name cannot be empty")
	}
	return &ExecutionTarget{Provider: provider, Name: name}, nil
}

// AgentTargetSchemaProperty is shared by every agent-to-agent tool schema.
// ToolTargetSchemaProperty is the per-call target of a data tool: which
// connected cluster or account to query instead of the conversation's own.
func ToolTargetSchemaProperty() ToolSchemaProperty {
	return ToolSchemaProperty{
		Type:        ToolSchemaTypeObject,
		Description: "Optional connected cluster or cloud account to query instead of the conversation's own: {provider: kubernetes|aws|gcp|azure, name: exact connected name}. Use it when the question names another cluster or account; omit it otherwise. This does not grant access.",
	}
}

func AgentTargetSchemaProperty() ToolSchemaProperty {
	return ToolSchemaProperty{
		Type:        ToolSchemaTypeObject,
		Description: "Optional target for this sub-agent: {provider: kubernetes|aws|gcp|azure, name: exact connected cluster/account name}. Split work naming multiple connected accounts into separate targeted child calls. Omit only for discovery without a named connection. This does not grant access.",
	}
}

// Kind names what a target is, for prompt text.
func (t ExecutionTarget) Kind() string {
	switch t.Provider {
	case "kubernetes":
		return "Kubernetes cluster"
	case "aws":
		return "AWS account"
	case "gcp":
		return "GCP account"
	case "azure":
		return "Azure account"
	}
	return "connected account"
}

// ScopeNote tells a planner or query writer that the target is already
// applied. Without it, a model that still sees the target's name in the user's
// question turns it into a filter (e.g. namespace="k8s-prod") on a backend that
// is already that cluster's own.
func (t *ExecutionTarget) ScopeNote() string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("Scope: this request already runs against the %s %q, using its own backends and credentials. %q is the name of that whole %s, not a namespace, label, project, service or filter value; do not add it to any query.", t.Kind(), t.Name, t.Name, t.Kind())
}
