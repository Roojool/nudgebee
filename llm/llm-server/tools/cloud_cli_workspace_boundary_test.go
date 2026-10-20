package tools

import (
	"strings"
	"testing"

	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
	"nudgebee/llm/workspace"

	"github.com/stretchr/testify/require"
)

func cloudCliToolContext(accountID, targetID, targetName string, tool core.NBTool) core.NbToolContext {
	ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{accountID, targetID})
	result := core.NewNbToolContext(ctx, tool, accountID, "user", "conversation", "message", "agent", "", nil, "", core.NBQueryConfig{}, "call")
	result.ToolConfig = core.ToolConfig{Name: targetName, Values: []core.ToolConfigValue{{Name: "id", Value: targetID}}}
	return result
}

func TestGcpAndAzurePerCallAccountSelection(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		command     string
		currentName string
		currentID   string
		targetName  string
		targetID    string
		newTool     func(*targetWorkspaceCapture) core.NBTool
	}{
		{name: "gcp", toolName: ToolExecuteGcpCliCommand, command: "gcloud projects list", currentName: "gcp-prod", currentID: "gcp-prod-id", targetName: "gcp-dev", targetID: "gcp-dev-id", newTool: func(c *targetWorkspaceCapture) core.NBTool { return GcpCliTool{workspaceManager: c} }},
		{name: "azure", toolName: ToolExecuteAzureCliCommand, command: "az vm list", currentName: "azure-prod", currentID: "azure-prod-id", targetName: "azure-dev", targetID: "azure-dev-id", newTool: func(c *targetWorkspaceCapture) core.NBTool { return AzureCliTool{workspaceManager: c} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const origin = "workspace-id"
			current := awsCfg(tt.currentName, tt.currentID, strings.TrimSuffix(tt.currentID, "-id"))
			target := awsCfg(tt.targetName, tt.targetID, strings.TrimSuffix(tt.targetID, "-id"))
			StubTenantAccounts(t, "tenant", map[string][]core.ToolConfig{tt.toolName: {current, target}})
			capture := &targetWorkspaceCapture{}
			tool := tt.newTool(capture)
			ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{origin, tt.currentID, tt.targetID})
			nbCtx := core.NewNbToolContext(ctx, tool, origin, "user", "conversation", "message", "agent", "", nil, "", core.NBQueryConfig{}, "call")
			nbCtx.ToolConfig = current

			resp, err := tool.Call(nbCtx, core.NBToolCallRequest{Command: tt.command, Arguments: map[string]any{"account": tt.targetName}})
			require.NoError(t, err)
			require.Equal(t, tt.targetName, capture.env[workspace.ENV_NB_TOOL_CONFIG_NAME])
			require.Equal(t, tt.targetID, capture.env[workspace.ENV_NB_TARGET_ACCOUNT_ID])
			require.True(t, strings.HasPrefix(resp.Data, "[account: "+tt.targetName+"]\n"), resp.Data)
			require.Equal(t, tt.toolName+"@"+tt.targetName, tool.(core.ToolConfirmationScope).ConfirmationKey(`{"command":"`+tt.command+`","account":"`+tt.targetName+`"}`))
		})
	}
}

func TestShellGcpAndAzureAccountArgumentRoutesShim(t *testing.T) {
	tests := []struct {
		name, toolName, command, targetName, targetID string
	}{
		{name: "gcp", toolName: ToolExecuteGcpCliCommand, command: "gcloud projects list", targetName: "gcp-dev", targetID: "gcp-dev-id"},
		{name: "azure", toolName: ToolExecuteAzureCliCommand, command: "az vm list", targetName: "azure-dev", targetID: "azure-dev-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const origin = "workspace-id"
			StubTenantAccounts(t, "tenant", map[string][]core.ToolConfig{tt.toolName: {awsCfg(tt.targetName, tt.targetID, tt.targetID)}})
			capture := &targetWorkspaceCapture{}
			tool := ShellTool{AccountId: origin, workspaceManager: capture}
			ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{origin, tt.targetID})
			nbCtx := core.NewNbToolContext(ctx, tool, origin, "user", "conversation", "message", "agent", "", nil, "", core.NBQueryConfig{}, "call")

			_, err := tool.Call(nbCtx, core.NBToolCallRequest{Command: tt.command, Arguments: map[string]any{"account": tt.targetName}})
			require.NoError(t, err)
			require.Equal(t, tt.targetName, capture.env[workspace.ENV_NB_TOOL_CONFIG_NAME])
			require.Equal(t, tt.targetID, capture.env[workspace.ENV_NB_TARGET_ACCOUNT_ID])
			require.Equal(t, core.ToolExecuteShellCommand+"@"+tt.targetName, tool.ConfirmationKey(`{"command":"`+tt.command+`","account":"`+tt.targetName+`"}`))
		})
	}
}

func TestDedicatedCloudToolsDoNotInjectCredentialsIntoWorkspace(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		targetID   string
		targetName string
		call       func(*targetWorkspaceCapture) (core.NBToolResponse, error)
	}{
		{name: "gcp", command: "gcloud projects list", targetID: "gcp-id", targetName: "gcp-prod"},
		{name: "azure", command: "az vm list", targetID: "azure-id", targetName: "azure-prod"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &targetWorkspaceCapture{}
			var err error
			if tt.name == "gcp" {
				tool := GcpCliTool{workspaceManager: capture}
				_, err = tool.Call(cloudCliToolContext("workspace", tt.targetID, tt.targetName, tool), core.NBToolCallRequest{Command: tt.command})
			} else {
				tool := AzureCliTool{workspaceManager: capture}
				_, err = tool.Call(cloudCliToolContext("workspace", tt.targetID, tt.targetName, tool), core.NBToolCallRequest{Command: tt.command})
			}
			require.NoError(t, err)
			require.Equal(t, tt.command, capture.command)
			require.Equal(t, tt.targetName, capture.env[workspace.ENV_NB_TOOL_CONFIG_NAME])
			require.Equal(t, tt.targetID, capture.env[workspace.ENV_NB_TARGET_ACCOUNT_ID])
			for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GCP_SA_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_CONFIG_DIR"} {
				require.NotContains(t, capture.env, key)
			}
		})
	}
}
