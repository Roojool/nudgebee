package prompts

import (
	"context"
	"strings"
	"testing"
)

func TestAwsLeanPromptIsProtocolNeutralAndDomainScoped(t *testing.T) {
	oldLoader := GetLoader()
	t.Cleanup(func() { SetGlobalLoaderForTesting(oldLoader) })
	SetGlobalLoaderForTesting(NewLoaderForTesting())

	prompt, err := GetPromptStrict(context.Background(), PromptAwsLean, "")
	if err != nil {
		t.Fatalf("load aws lean prompt: %v", err)
	}

	for _, protocolToken := range []string{"<thought_action>", "<action>", "<tool_name>", "<tool_input>"} {
		if strings.Contains(prompt, protocolToken) {
			t.Errorf("aws domain prompt must not prescribe planner protocol %q", protocolToken)
		}
	}
	if strings.Contains(prompt, "full read access") {
		t.Error("aws prompt must not promise access that the configured tools may not have")
	}

	for _, required := range []string{
		"AWS EXECUTION EFFICIENCY:",
		"Use `logs`, `events`, `metrics`, `traces`, and `service_dependency_graph` for observability evidence",
		"Use workspace AWS CLI commands for resource state, configuration, declared intent, and requested operations",
		"Do not reproduce an available observability tool's work through the AWS CLI",
		"Use the available workspace shell for AWS CLI commands and local computation",
		"Combine related commands, filtering, aggregation, or scripting",
		"multiple requested values can be derived mechanically from the same source response",
		"Keep shell output limited to the evidence and mechanical aggregation needed for the current question",
		"This does not restrict the evidence synthesis required in your final answer",
		"Shell commands are classified before execution",
		"mutations and commands with uncertain effects automatically use the normal approval and resume flow",
		"Parallel investigation example (illustrative, not mandatory)",
		"`events`, `logs`, `metrics`, `traces`, and `service_dependency_graph`",
		"Use only the evidence domains relevant to the question",
		"These siblings share a subject but do not depend on one another",
		"cloud_resource_search_execute",
		"finops",
		"automation",
		"If nothing matches, run the AWS CLI command through the workspace shell",
		"service_dependency_graph",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("aws prompt missing domain policy %q", required)
		}
	}
}
