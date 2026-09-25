//go:build e2e

package tools

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
)

// TestVMToolsLive exercises inventory selection and one read-only SSH command.
// Set VM_TOOLS_E2E=1 and VM_E2E_ACCOUNT to opt in against a configured VM account.
func TestVMToolsLive(t *testing.T) {
	if os.Getenv("VM_TOOLS_E2E") != "1" {
		t.Skip("set VM_TOOLS_E2E=1 to run live VM tool test")
	}
	tenant, user, account := os.Getenv("TEST_TENANT"), os.Getenv("TEST_USER"), os.Getenv("VM_E2E_ACCOUNT")
	require.NotEmpty(t, tenant, "TEST_TENANT")
	require.NotEmpty(t, user, "TEST_USER")
	require.NotEmpty(t, account, "VM_E2E_ACCOUNT")
	sc := security.NewRequestContextForTenantAccountAdmin(tenant, user, []string{account})
	list := VMListTool{}
	ctx := core.NewNbToolContext(sc, list, account, user, uuid.NewString(), uuid.NewString(), uuid.NewString(), "", nil, "", core.NBQueryConfig{}, "")
	result, err := list.Call(ctx, core.NBToolCallRequest{})
	require.NoError(t, err)
	require.Equal(t, core.NBToolResponseStatusSuccess, result.Status, result.Data)
	var inventory struct {
		VMs []vmInventoryRow `json:"vms"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Data), &inventory))
	require.NotEmpty(t, inventory.VMs)
	resourceID := os.Getenv("VM_E2E_RESOURCE_ID")
	if resourceID == "" {
		t.Logf("VM inventory lists %d targets; set VM_E2E_RESOURCE_ID to test read-only SSH", len(inventory.VMs))
		return
	}
	found := false
	for _, vm := range inventory.VMs {
		if vm.ID == resourceID {
			found = true
			break
		}
	}
	require.True(t, found, "VM_E2E_RESOURCE_ID must be returned by vm_list_execute")
	execute := VMExecuteTool{}
	ctx = core.NewNbToolContext(sc, execute, account, user, uuid.NewString(), uuid.NewString(), uuid.NewString(), "", nil, "", core.NBQueryConfig{}, "")
	result, err = execute.Call(ctx, core.NBToolCallRequest{Arguments: map[string]any{"resource_id": resourceID, "command": "uname -s"}})
	require.NoError(t, err)
	require.Equal(t, core.NBToolResponseStatusSuccess, result.Status, result.Data)
	var output struct {
		ResourceID   string `json:"resource_id"`
		DatasourceID string `json:"datasource_id"`
		Stdout       string `json:"stdout"`
		ExitCode     int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Data), &output))
	require.Equal(t, resourceID, output.ResourceID)
	require.NotEmpty(t, output.DatasourceID)
	require.Equal(t, 0, output.ExitCode)
	require.Contains(t, output.Stdout, "Linux")

	result, err = execute.Call(ctx, core.NBToolCallRequest{Arguments: map[string]any{"resource_id": uuid.NewString(), "command": "uname -s"}})
	require.Error(t, err)
	require.Equal(t, core.NBToolResponseStatusError, result.Status)
}
