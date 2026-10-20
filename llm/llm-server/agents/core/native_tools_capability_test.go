package core

import "testing"

func TestSupportsNativeTools(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{"bedrock", "bedrock", "anthropic.claude-3", true},
		{"azure", "azure", "gpt-4o", true},
		{"googleai", "googleai", "gemini-2.0-flash", true},
		{"vertexai", "vertexai", "gemini-1.5-pro", true},
		{"vertexai_endpoint", "vertexai_endpoint", "some-model", true},
		{"openai", "openai", "gpt-4o", true},
		{"anthropic", "anthropic", "claude-3-5-sonnet", true},
		{"sagemaker not supported", "sagemaker", "llama", false},
		{"huggingface not supported", "huggingface", "meta-llama/Llama-3.3", false},
		{"unknown fails closed", "some-future-provider", "x", false},
		{"empty fails closed", "", "", false},
		{"case insensitive", "Bedrock", "m", true},
		{"whitespace trimmed", "  azure  ", "m", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsNativeTools(tt.provider, tt.model); got != tt.want {
				t.Errorf("SupportsNativeTools(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestShouldUseReAct4(t *testing.T) {
	tests := []struct {
		name     string
		declared AgentPlannerType
		engine   string
		pinned   bool
		provider string
		want     bool
	}{
		{"engine react3, native provider, react intent", AgentPlannerTypeReAct, PlannerEngineReAct3, false, "bedrock", false},
		{"engine auto, react intent, native", AgentPlannerTypeReAct, PlannerEngineAuto, false, "bedrock", true},
		{"engine auto, orchestrating intent, native", AgentPlannerTypeOrchestrating, PlannerEngineAuto, false, "azure", true},
		{"engine react4, orchestrating intent, native", AgentPlannerTypeOrchestrating, PlannerEngineReAct4, false, "azure", true},
		{"default empty engine (auto), react intent, native", AgentPlannerTypeReAct, "", false, "bedrock", true},
		{"engine auto, react intent, non-native provider", AgentPlannerTypeReAct, PlannerEngineAuto, false, "sagemaker", false},
		{"engine auto, react intent, unknown provider", AgentPlannerTypeReAct, PlannerEngineAuto, false, "mystery", false},
		{"engine auto, tool intent excluded", AgentPlannerTypeTool, PlannerEngineAuto, false, "bedrock", false},
		{"engine auto, custom intent excluded", AgentPlannerTypeCustom, PlannerEngineAuto, false, "bedrock", false},
		{"engine auto, classification intent excluded", AgentPlannerTypeClassification, PlannerEngineAuto, false, "bedrock", false},
		{"engine auto, conversational intent excluded", AgentPlannerTypeConversational, PlannerEngineAuto, false, "bedrock", false},
		// The react_3/react_4 engine values are never declared by an agent, so
		// they must not opt into react_4 routing either.
		{"engine auto, react3 engine value excluded", AgentPlannerTypeReAct3, PlannerEngineAuto, false, "bedrock", false},

		// A pinned agent (NBAgentReAct4Provider, e.g. k8s_orchestrator_react4)
		// bypasses the engine react3 override but nothing else.
		{"pinned, engine react3, native provider", AgentPlannerTypeOrchestrating, PlannerEngineReAct3, true, "bedrock", true},
		{"pinned, engine react3, react intent, native", AgentPlannerTypeReAct, PlannerEngineReAct3, true, "azure", true},
		// ...the capability gate still applies: no native tool support -> ReAct3.
		{"pinned, engine react3, non-native provider", AgentPlannerTypeOrchestrating, PlannerEngineReAct3, true, "sagemaker", false},
		{"pinned, engine react3, unknown provider fails closed", AgentPlannerTypeOrchestrating, PlannerEngineReAct3, true, "mystery", false},
		// ...and so does the declared-intent gate.
		{"pinned, engine react3, tool intent still excluded", AgentPlannerTypeTool, PlannerEngineReAct3, true, "bedrock", false},

		// Unknown/invalid engine values fail closed to ReAct3 unless pinned.
		{"unknown engine react33 fails closed on native provider", AgentPlannerTypeOrchestrating, "react33", false, "azure", false},
		{"unknown engine custom fails closed on native provider", AgentPlannerTypeReAct, "custom", false, "bedrock", false},
		{"unknown engine react33 pinned honors native capability", AgentPlannerTypeOrchestrating, "react33", true, "azure", true},
		{"unknown engine react33 pinned non-native fails closed", AgentPlannerTypeOrchestrating, "react33", true, "sagemaker", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseReAct4(tt.declared, tt.engine, tt.pinned, tt.provider, ""); got != tt.want {
				t.Errorf("shouldUseReAct4(%q, engine=%v, pinned=%v, %q) = %v, want %v",
					tt.declared, tt.engine, tt.pinned, tt.provider, got, tt.want)
			}
		})
	}
}
