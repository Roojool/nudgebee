//go:build e2e

package agents

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nudgebee/llm/agents/core"
	"nudgebee/llm/security"
	"nudgebee/llm/tools"
)

// TestVMOrchestratorLiveSetup checks routing, prompt loading, and tool availability
// for a real VM account before an LLM conversation is attempted.
func TestVMOrchestratorLiveSetup(t *testing.T) {
	if os.Getenv("VM_TOOLS_E2E") != "1" {
		t.Skip("set VM_TOOLS_E2E=1 to check live VM orchestrator setup")
	}
	tenant, user, account := os.Getenv("TEST_TENANT"), os.Getenv("TEST_USER"), os.Getenv("VM_E2E_ACCOUNT")
	require.NotEmpty(t, tenant, "TEST_TENANT")
	require.NotEmpty(t, user, "TEST_USER")
	require.NotEmpty(t, account, "VM_E2E_ACCOUNT")
	sc := security.NewRequestContextForTenantAccountAdmin(tenant, user, []string{account})
	require.Equal(t, AgentVMOrchestratorName, GetDebugAgentName(account))
	agent, ok := core.GetNBAgent(sc, AgentVMOrchestratorName, account, "")
	require.True(t, ok)
	query := core.NBAgentRequest{AccountId: account}
	prompt := agent.GetSystemPrompt(sc, query)
	require.NotEmpty(t, prompt)
	available := map[string]bool{}
	for _, tool := range agent.GetSupportedTools(sc) {
		available[tool.Name()] = true
	}
	require.True(t, available[tools.ToolVMList], "vm_list_execute unavailable")
	require.True(t, available[tools.ToolVMExecute], "vm_execute unavailable")
}

// TestVMOrchestratorLiveConversation runs a read-only fleet query through the
// conversation planner. It is separately opted in because it calls an LLM.
func TestVMOrchestratorLiveConversation(t *testing.T) {
	if os.Getenv("VM_CONVERSATION_E2E") != "1" {
		t.Skip("set VM_CONVERSATION_E2E=1 to run a live VM conversation")
	}
	tenant, user, account := os.Getenv("TEST_TENANT"), os.Getenv("TEST_USER"), os.Getenv("VM_E2E_ACCOUNT")
	require.NotEmpty(t, tenant)
	require.NotEmpty(t, user)
	require.NotEmpty(t, account)
	sc := security.NewRequestContextForTenantAccountAdmin(tenant, user, []string{account})
	agent := VMOrchestratorAgent{accountID: account}
	response, err := core.HandleConversationSessionRequest(sc, agent, user, account, "vm-e2e-"+uuid.NewString(), "List the VMs in this account")
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, AgentVMOrchestratorName, response.AgentName)
	require.Equal(t, core.ConversationStatusCompleted, response.Status)
	require.NotEmpty(t, response.Response)
	foundInventoryCall := false
	var toolNames []string
	for _, step := range response.AgentStepResponse {
		if step.Call.FunctionCall == nil {
			continue
		}
		toolNames = append(toolNames, step.Call.FunctionCall.Name)
		if step.Call.FunctionCall.Name == tools.ToolVMList && !step.Failed {
			foundInventoryCall = true
		}
		require.NotEqual(t, tools.ToolVMExecute, step.Call.FunctionCall.Name, "a listing must not run remote SSH")
	}
	require.True(t, foundInventoryCall, "fleet listing must use vm_list_execute in this turn")
	t.Logf("conversation=%s session=%s status=%v tools=%v response_length=%d", response.ConversationId, response.SessionId, response.Status, toolNames, len(response.Response))
}
