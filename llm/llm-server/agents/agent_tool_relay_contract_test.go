package agents

import (
	"nudgebee/llm/agents/core"
	"nudgebee/llm/security"
	"testing"

	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
)

// TestAgentToolOutputs_CarryRelayContract pins the relay contract on the
// agent-as-tool interfaces whose formatted answers were observed being
// re-encoded by a calling orchestrator: k8s_orchestrator received finops's
// markdown table (citations and all) and rewrote it into a raw JSON object as
// the user-facing reply. The tool's rendered Description — the one interface
// the caller actually reads — must keep telling callers the output is
// user-ready markdown to relay, not data to restructure.
func TestAgentToolOutputs_CarryRelayContract(t *testing.T) {
	for _, name := range []string{FinOpsAgentName} {
		tool, ok := toolcore.GetNBTool("relay-contract-test", name)
		assert.True(t, ok, "%s must be registered as a tool", name)
		desc := tool.Description()
		assert.Contains(t, desc, "markdown", "%s tool output must declare markdown", name)
		assert.Contains(t, desc, "relay the markdown as-is", "%s tool output must carry the relay instruction", name)
		assert.Contains(t, desc, "do NOT re-encode it into JSON", "%s tool output must forbid re-encoding", name)
	}
}

// TestRetiredRecommendationsAgentResolvesToFinOps pins the retirement: the old
// name still resolves as an agent (stored history, @recommendations) and as a
// tool alias (tool calls replayed from history), but is not a separate agent.
func TestRetiredRecommendationsAgentResolvesToFinOps(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	agent, ok := core.GetNBAgent(ctx, RecommendationsAgentName, "relay-contract-test", "")
	assert.True(t, ok, "the retired name must still resolve as an agent")
	assert.Equal(t, FinOpsAgentName, agent.GetName())
	assert.True(t, core.IsSystemAgentAlias(RecommendationsAgentName), "the retired name is an alias, not a picker entry")
	tool, ok := toolcore.GetNBTool("relay-contract-test", RecommendationsAgentName)
	assert.True(t, ok, "a tool call named after the retired agent must resolve")
	assert.Equal(t, FinOpsAgentName, tool.Name())
}
