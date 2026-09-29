package agents

import (
	"nudgebee/llm/agents/core"
	"nudgebee/llm/tools"
	toolcore "nudgebee/llm/tools/core"
)

// Tools whose answer does not depend on the account they run under. A child
// targeted at another cluster or cloud account may still use them; every
// other tool that cannot take the target is refused there, so it cannot
// answer from the conversation's own account as if it were the target's.
// Add a tool here only if it reads no account-scoped data.
func init() {
	toolcore.MarkTargetNeutral(
		// Reasoning, conversation state and clarification.
		core.ToolLlm,
		tools.ThinkToolName,
		toolcore.NotebookToolName,
		tools.MermaidValidationToolName,
		FollowupAgentName,
		// Tool and skill discovery. Skills are guidance, not account data.
		SearchToolsToolName,
		tools.SearchSkillsToolName,
		tools.LoadSkillsToolName,
		// Documentation and the web.
		tools.SearchDocsToolName,
		WebSearchAgentName,
		tools.ToolExecuteSearchCommand,
		tools.ToolExecuteCrawlCommand,
		// Tenant-wide inventory, graph and product state.
		tools.ToolCloudResourceSearch,
		tools.ToolKGSearchNodes,
		tools.ToolKGTraverse,
		tools.ToolKGGetNode,
		NudgebeeAgentName,
		// Tenant-wide integrations.
		GithubAgentName,
		GitlabAgentName,
		tools.ToolExecuteGithubCliCommand,
		tools.ToolExecuteGitlabCliCommand,
		TicketsAgentName,
		TicketsV2AgentName,
		tools.TicketMasterToolName,
		tools.TicketMasterToolNameV2,
		// Local processing of files already in the workspace.
		tools.ToolStandardDiagnosticGrep,
		VisualizationAgentName,
		PromptRefinementAgentName,
	)
}
