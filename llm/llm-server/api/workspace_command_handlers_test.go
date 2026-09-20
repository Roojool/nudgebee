package api

import (
	"errors"
	"nudgebee/llm/cloud"
	"nudgebee/llm/security"
	"nudgebee/llm/tools"
	"nudgebee/llm/tools/core"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func awsTarget(name, id, number, region string) core.ToolConfig {
	return core.ToolConfig{Name: name, Values: []core.ToolConfigValue{
		{Name: "id", Value: id},
		{Name: "account_number", Value: number},
		{Name: "region", Value: region},
	}}
}

func stubAwsWorkspaceTargets(t *testing.T, configs []core.ToolConfig) {
	t.Helper()
	original := tools.TenantAccountLister
	tools.TenantAccountLister = func(_ *security.RequestContext, _ core.NBTool) ([]core.ToolConfig, error) {
		return configs, nil
	}
	t.Cleanup(func() { tools.TenantAccountLister = original })
}

func TestAwsWorkspaceHandlerExtractsProfileAndResolvesTarget(t *testing.T) {
	ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"workspace", "prod-id"})
	stubAwsWorkspaceTargets(t, []core.ToolConfig{
		awsTarget("development", "dev-id", "111111111111", "us-east-1"),
		awsTarget("production", "prod-id", "222222222222", "eu-west-1"),
	})

	plan, err := (awsWorkspaceCommandHandler{}).Prepare(ctx, core.NbToolContext{}, WorkspaceExecuteRequest{
		AccountId: "workspace",
		Command:   "aws ec2 describe-instances --profile production --region eu-west-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "prod-id", plan.TargetAccountID)
	assert.Equal(t, "production", plan.Request.TargetName)
	assert.Equal(t, "aws 'ec2' 'describe-instances' '--region' 'eu-west-1'", plan.Request.Command)
}

func TestAwsWorkspaceHandlerRejectsConflictingProfiles(t *testing.T) {
	_, err := (awsWorkspaceCommandHandler{}).Prepare(nil, core.NbToolContext{}, WorkspaceExecuteRequest{
		Command: "aws --profile development ec2 describe-instances --profile=production",
	})
	require.ErrorContains(t, err, "more than one profile")
}

func TestVirtualAwsProfilesOnlyListsReadableTargets(t *testing.T) {
	ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"workspace", "prod-id"})
	stubAwsWorkspaceTargets(t, []core.ToolConfig{
		awsTarget("development", "dev-id", "111111111111", "us-east-1"),
		awsTarget("production", "prod-id", "222222222222", "eu-west-1"),
	})

	plan, err := (awsWorkspaceCommandHandler{}).Prepare(ctx, core.NbToolContext{}, WorkspaceExecuteRequest{
		AccountId: "workspace",
		Command:   "aws configure list-profiles",
	})
	require.NoError(t, err)
	require.True(t, plan.Virtual)
	assert.Equal(t, "production\n", plan.VirtualResult)
}

func TestVirtualAwsRegionUsesSelectedProfile(t *testing.T) {
	ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"workspace", "prod-id"})
	stubAwsWorkspaceTargets(t, []core.ToolConfig{awsTarget("production", "prod-id", "222222222222", "eu-west-1")})

	plan, err := (awsWorkspaceCommandHandler{}).Prepare(ctx, core.NbToolContext{}, WorkspaceExecuteRequest{
		AccountId: "workspace",
		Command:   "aws configure get region --profile production",
	})
	require.NoError(t, err)
	require.True(t, plan.Virtual)
	assert.Equal(t, "eu-west-1\n", plan.VirtualResult)
}

func TestAwsWorkspaceHandlerPreservesCloudProcessResult(t *testing.T) {
	original := executeCloudCliV2
	t.Cleanup(func() { executeCloudCliV2 = original })
	var captured cloud.CloudExecuteCliCommandRequest
	executeCloudCliV2 = func(req cloud.CloudExecuteCliCommandRequest) (cloud.CliExecutionResult, error) {
		captured = req
		return cloud.CliExecutionResult{Stdout: "partial", Stderr: "denied", ExitCode: 7}, nil
	}
	ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"aws-id"})
	plan := workspaceCommandPlan{
		Request:         WorkspaceExecuteRequest{Command: "aws s3 ls"},
		TargetAccountID: "aws-id",
	}
	result, err := (awsWorkspaceCommandHandler{}).Execute(ctx, core.NbToolContext{}, plan)
	var exitErr *tools.RelayCommandExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 7, exitErr.ExitCode)
	assert.Equal(t, "aws-id", captured.AccountID)
	assert.Equal(t, "tenant", captured.TenantID)
	assert.Equal(t, "user", captured.UserID)
	assert.Equal(t, map[string]any{
		"stdout": "partial", "stderr": "denied", "exit_code": 7, "timed_out": false, "output_truncated": false,
	}, result)
}

func TestKubernetesWorkspaceHandlerPreservesContextRouting(t *testing.T) {
	ctx := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"workspace", "dev-id"})
	stubTenantAccounts(t, "workspace", "dev-id")
	tools.StubTenantClusters(t, "tenant", []core.ToolConfig{
		{Name: "k8s-prod", Values: []core.ToolConfigValue{{Name: "id", Value: "workspace"}}},
		{Name: "k8s-dev", Values: []core.ToolConfigValue{{Name: "id", Value: "dev-id"}}},
	})
	toolCtx := core.NbToolContext{
		Ctx:       ctx,
		AccountId: "workspace",
		ToolConfig: core.ToolConfig{
			Name: "k8s-prod", Values: []core.ToolConfigValue{{Name: "id", Value: "workspace"}},
		},
	}
	plan, err := (kubernetesWorkspaceCommandHandler{}).Prepare(ctx, toolCtx, WorkspaceExecuteRequest{
		AccountId: "workspace",
		Command:   "kubectl --context k8s-dev get pods",
	})
	require.NoError(t, err)
	assert.Equal(t, "dev-id", plan.TargetAccountID)
	assert.Equal(t, "k8s-dev", plan.Request.TargetName)
	assert.Equal(t, "kubectl 'get' 'pods'", plan.Request.Command)
}
