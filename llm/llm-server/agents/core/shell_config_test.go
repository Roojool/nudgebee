package core

import (
	"github.com/stretchr/testify/require"
	"nudgebee/llm/common"
	"nudgebee/llm/security"
	"nudgebee/llm/tools"
	toolcore "nudgebee/llm/tools/core"
	"testing"
)

func TestShellConfigOwnersDoNotChangeExecutionAction(t *testing.T) {
	shell := tools.ShellTool{}
	seen := map[string]bool{}
	for _, tc := range []struct{ input, owner string }{
		{`{"command":"aws s3 ls"}`, tools.ToolExecuteAwsCliCommand},
		{`{"command":"gcloud projects list"}`, tools.ToolExecuteGcpCliCommand},
		{`{"command":"kubectl get pods"}`, tools.ToolExecuteKubectlCommand},
	} {
		action := NBAgentPlannerToolAction{Tool: "shell_execute", ToolID: "original-call", ToolInput: tc.input}
		owner := configOwnerForAction(shell, "account", action.ToolInput)
		require.Equal(t, tc.owner, owner.Name())
		require.False(t, seen[owner.Name()])
		seen[owner.Name()] = true
		_, unresolved := unresolvedConfigForActionTool(owner, nil, 2)
		require.True(t, unresolved)
		require.Equal(t, "shell_execute", action.Tool)
		require.Equal(t, "original-call", action.ToolID)
	}
	require.Equal(t, "shell_execute", configOwnerForAction(shell, "account", "cat pods.json").Name())
}
func TestShellConfigRejectsStaleSelectionBeforeExecution(t *testing.T) {
	const account = "shell-stale-config-test"
	configs := []toolcore.ToolConfig{{Name: "available", Values: []toolcore.ToolConfigValue{{Name: "id", Value: "target"}}}}
	data, err := common.MarshalJson(configs)
	require.NoError(t, err)
	require.NoError(t, common.CacheSet(toolcore.CacheNamespaceLlmToolConfig, "list_tool_configs:"+account+":aws_execute", data))
	e := plannerExecutor{ctx: security.NewRequestContextForSuperAdmin(), agentRequest: NBAgentRequest{AccountId: account, QueryConfig: toolcore.NBQueryConfig{ToolConfigs: map[string]string{"aws_execute": "removed"}}}}
	owner := configOwnerForAction(tools.ShellTool{}, account, "aws s3 ls")
	_, finish, err := e.followupForMultipleToolConfigs(owner, NBAgentPlannerToolAction{Tool: "shell_execute", ToolID: "call", ToolInput: "aws s3 ls"})
	require.ErrorContains(t, err, "unavailable or unauthorized")
	require.Nil(t, finish)
}

func TestShellConfigCrossAccountExplicitTarget(t *testing.T) {
	const tenant = "tenant-184"
	const user = "user-1"
	const k8sAccount = "cluster-account-id"
	const gcpAccount = "gcp-account-id"
	const awsAccount = "aws-account-id"

	gcpConfigs := []toolcore.ToolConfig{
		{Name: "iteration-gcp-test-184", Values: []toolcore.ToolConfigValue{{Name: "id", Value: gcpAccount}}},
	}
	awsConfigs := []toolcore.ToolConfig{
		{Name: "iteration-aws-test-184", Values: []toolcore.ToolConfigValue{{Name: "id", Value: awsAccount}}},
	}

	tools.StubTenantAccounts(t, tenant, map[string][]toolcore.ToolConfig{
		tools.ToolExecuteGcpCliCommand: gcpConfigs,
		tools.ToolExecuteAwsCliCommand: awsConfigs,
	})

	sc := security.NewRequestContextForTenantAccountAdmin(tenant, user, []string{k8sAccount, gcpAccount, awsAccount})
	e := plannerExecutor{
		ctx: sc,
		agentRequest: NBAgentRequest{
			AccountId: k8sAccount,
		},
	}

	t.Run("GCP explicit account argument in cluster conversation", func(t *testing.T) {
		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-gcp",
			ToolInput: `{"command":"gcloud config get-value account","account":"iteration-gcp-test-184"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)
		require.Equal(t, tools.ToolExecuteGcpCliCommand, owner.Name())

		steps, finish, err := e.followupForMultipleToolConfigs(owner, action)
		require.NoError(t, err)
		require.Nil(t, finish)
		require.Empty(t, steps, "explicit valid target should resolve without follow-up prompt or error")
	})

	t.Run("AWS explicit profile flag in cluster conversation", func(t *testing.T) {
		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-aws",
			ToolInput: `{"command":"aws --profile iteration-aws-test-184 sts get-caller-identity"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)
		require.Equal(t, tools.ToolExecuteAwsCliCommand, owner.Name())

		steps, finish, err := e.followupForMultipleToolConfigs(owner, action)
		require.NoError(t, err)
		require.Nil(t, finish)
		require.Empty(t, steps, "explicit valid AWS profile should resolve without follow-up prompt or error")
	})

	t.Run("Unknown account refusal lists available accounts", func(t *testing.T) {
		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-unknown",
			ToolInput: `{"command":"gcloud config get-value account","account":"iteration-GCP-test-182"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)

		steps, finish, err := e.followupForMultipleToolConfigs(owner, action)
		require.NoError(t, err)
		require.Nil(t, finish)
		require.Len(t, steps, 1)
		require.Equal(t, ToolStatusFailure, steps[0].Status)
		require.Contains(t, steps[0].Observation, `account "iteration-GCP-test-182" is not connected or you do not have access to it; available accounts: iteration-gcp-test-184`)
	})

	t.Run("Missing account in cluster conversation informs user to specify account", func(t *testing.T) {
		emptyConfigs, mErr := common.MarshalJson([]toolcore.ToolConfig{})
		require.NoError(t, mErr)
		require.NoError(t, common.CacheSet(toolcore.CacheNamespaceLlmToolConfig, "list_tool_configs:"+k8sAccount+":"+tools.ToolExecuteGcpCliCommand, emptyConfigs))

		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-missing-account",
			ToolInput: `{"command":"gcloud compute instances list"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)

		steps, finish, err := e.followupForMultipleToolConfigs(owner, action)
		require.NoError(t, err)
		require.Nil(t, finish)
		require.Len(t, steps, 1)
		require.Equal(t, ToolStatusFailure, steps[0].Status)
		require.Contains(t, steps[0].Observation, "not reachable from this conversation's account. Specify an account to target; available accounts: iteration-gcp-test-184")
	})

	t.Run("Missing account deterministically sorts available accounts across runs", func(t *testing.T) {
		const gcpAccount1 = "gcp-1"
		const gcpAccount2 = "gcp-2"
		const gcpAccount3 = "gcp-3"
		tools.StubTenantAccounts(t, tenant, map[string][]toolcore.ToolConfig{
			tools.ToolExecuteGcpCliCommand: {
				{Name: "zebra-gcp", Values: []toolcore.ToolConfigValue{{Name: "id", Value: gcpAccount1}}},
				{Name: "alpha-gcp", Values: []toolcore.ToolConfigValue{{Name: "id", Value: gcpAccount2}}},
				{Name: "mango-gcp", Values: []toolcore.ToolConfigValue{{Name: "id", Value: gcpAccount3}}},
			},
		})
		scMulti := security.NewRequestContextForTenantAccountAdmin(tenant, user, []string{k8sAccount, gcpAccount1, gcpAccount2, gcpAccount3})
		eMulti := plannerExecutor{
			ctx: scMulti,
			agentRequest: NBAgentRequest{
				AccountId: k8sAccount,
			},
		}
		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-missing-account-multi",
			ToolInput: `{"command":"gcloud compute instances list"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)

		expected := "available accounts: alpha-gcp, mango-gcp, zebra-gcp"
		for i := 0; i < 5; i++ {
			steps, finish, err := eMulti.followupForMultipleToolConfigs(owner, action)
			require.NoError(t, err)
			require.Nil(t, finish)
			require.Len(t, steps, 1)
			require.Contains(t, steps[0].Observation, expected)
		}
	})

	t.Run("nil tool returns error", func(t *testing.T) {
		e := &plannerExecutor{}
		steps, finish, err := e.followupForMultipleToolConfigs(nil, NBAgentPlannerToolAction{})
		require.EqualError(t, err, "tool is nil")
		require.Nil(t, steps)
		require.Nil(t, finish)
	})

	t.Run("Failed tenant accounts lookup returns tool failure step", func(t *testing.T) {
		eNoTenant := &plannerExecutor{
			ctx:          security.NewRequestContextForSuperAdmin(),
			agentRequest: NBAgentRequest{AccountId: k8sAccount},
		}
		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-lookup-fail",
			ToolInput: `{"command":"gcloud compute instances list"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)
		steps, finish, err := eNoTenant.followupForMultipleToolConfigs(owner, action)
		require.NoError(t, err)
		require.Nil(t, finish)
		require.Len(t, steps, 1)
		require.Equal(t, ToolStatusFailure, steps[0].Status)
		require.Contains(t, steps[0].Observation, "Failed to list available accounts:")
	})

	t.Run("Invalid hint for shell target when no local configs returns tool failure step", func(t *testing.T) {
		eStale := &plannerExecutor{
			ctx:          sc,
			agentRequest: NBAgentRequest{AccountId: k8sAccount, QueryConfig: toolcore.NBQueryConfig{ToolConfigs: map[string]string{tools.ToolExecuteGcpCliCommand: "non-existent-account"}}},
		}
		action := NBAgentPlannerToolAction{
			Tool:      "shell_execute",
			ToolID:    "call-stale-hint",
			ToolInput: `{"command":"gcloud compute instances list"}`,
		}
		owner := configOwnerForAction(tools.ShellTool{}, k8sAccount, action.ToolInput)
		steps, finish, err := eStale.followupForMultipleToolConfigs(owner, action)
		require.NoError(t, err)
		require.Nil(t, finish)
		require.Len(t, steps, 1)
		require.Equal(t, ToolStatusFailure, steps[0].Status)
		require.Contains(t, steps[0].Observation, "shell: selected gcloud_execute configuration is unavailable or unauthorized")
	})
}
