package agents

import (
	"nudgebee/llm/agents/core"
	"nudgebee/llm/prompts"
	"nudgebee/llm/security"
	"nudgebee/llm/tools"
	toolcore "nudgebee/llm/tools/core"
)

const AgentVMOrchestratorName = "vm_orchestrator"

func init() {
	core.RegisterNBAgentFactoryWithAliases(AgentVMOrchestratorName, func(accountID string) (core.NBAgent, error) {
		return VMOrchestratorAgent{accountID: accountID}, nil
	}, "vm_debug")
}

type VMOrchestratorAgent struct{ accountID string }

func (a VMOrchestratorAgent) GetName() string { return AgentVMOrchestratorName }
func (a VMOrchestratorAgent) GetNameAliases() []string {
	return []string{"VM Troubleshooter", "VM Fleet"}
}
func (a VMOrchestratorAgent) GetDescription() string {
	return "Investigates an account-scoped fleet of virtual machines through verified inventory and target-bound SSH execution."
}
func (a VMOrchestratorAgent) GetPlannerType() core.AgentPlannerType {
	return core.AgentPlannerTypeOrchestrating
}
func (a VMOrchestratorAgent) GetModelCategory() core.ModelTier { return core.ModelTierReasoning }
func (a VMOrchestratorAgent) GetCacheScope() core.CacheScope   { return core.CacheScopeAccount }

func (a VMOrchestratorAgent) GetSupportedTools(ctx *security.RequestContext) []toolcore.NBTool {
	allowed := map[string]bool{
		tools.ToolVMList:                 true,
		tools.ToolVMExecute:              true,
		toolcore.ToolExecuteShellCommand: true,
		FinOpsAgentName:                  true,
		DelegateAgentToolName:            true,
		SearchToolsToolName:              true,
		tools.SearchSkillsToolName:       true,
		NudgebeeAgentName:                true,
	}
	var out []toolcore.NBTool
	for _, tool := range toolcore.GetEnabledNBTools(ctx, a.accountID) {
		if allowed[tool.Name()] {
			out = append(out, tool)
		}
	}
	return out
}

func (a VMOrchestratorAgent) GetSystemPrompt(ctx *security.RequestContext, query core.NBAgentRequest) core.NBAgentPrompt {
	content, err := prompts.GetPromptStrict(ctx.GetContext(), prompts.PromptVMLean, query.AccountId)
	if err != nil {
		ctx.GetLogger().Error("vm orchestrator: prompt load failed", "error", err)
		return core.NBAgentPrompt{}
	}
	return core.ParsePromptToNBAgentPrompt(content)
}
