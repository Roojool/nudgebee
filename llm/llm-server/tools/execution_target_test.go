package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
)

func TestDelegatedTargetIdentityCannotMoveAfterApproval(t *testing.T) {
	ctx := core.NbToolContext{QueryConfig: core.NBQueryConfig{DelegatedTarget: &core.ExecutionTarget{Provider: "aws", Name: "aws-prod", AccountID: "original-id"}}}
	original := core.ToolConfig{Name: "aws-prod", Values: []core.ToolConfigValue{{Name: "id", Value: "original-id"}}}
	replaced := core.ToolConfig{Name: "aws-prod", Values: []core.ToolConfigValue{{Name: "id", Value: "replacement-id"}}}
	other := core.ToolConfig{Name: "explicit-other", Values: []core.ToolConfigValue{{Name: "id", Value: "other-id"}}}
	require.NoError(t, VerifyDelegatedTargetIdentity(ctx, "aws", original, "aws-prod"))
	require.ErrorContains(t, VerifyDelegatedTargetIdentity(ctx, "aws", replaced, "aws-prod"), "changed since handoff")
	require.NoError(t, VerifyDelegatedTargetIdentity(ctx, "aws", original, ""))
	require.ErrorContains(t, VerifyDelegatedTargetIdentity(ctx, "aws", replaced, ""), "changed since handoff", "implicit defaults must still be identity-bound")
	require.NoError(t, VerifyDelegatedTargetIdentity(ctx, "aws", other, "explicit-other"), "an explicit override is a separate approval target")
	require.ErrorContains(t, VerifyDelegatedTargetIdentity(ctx, "aws", replaced, "explicit-other"), "changed since handoff", "a mismatched selector must not bypass the bound identity")
}

func TestTargetDataAccountIDFollowsTheDelegatedTarget(t *testing.T) {
	previous := TenantAccountLister
	TenantAccountLister = func(_ *security.RequestContext, tool core.NBTool) ([]core.ToolConfig, error) {
		if tool.Name() != ToolExecuteKubectlCommand {
			return nil, nil
		}
		return []core.ToolConfig{{Name: "k8s-prod", Values: []core.ToolConfigValue{{Name: "id", Value: "prod-id"}}}}, nil
	}
	t.Cleanup(func() { TenantAccountLister = previous })
	sc := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"origin-id", "prod-id"})
	ctx := core.NbToolContext{Ctx: sc, AccountId: "origin-id"}

	id, err := TargetDataAccountID(ctx)
	require.NoError(t, err)
	require.Equal(t, "origin-id", id, "no target: the conversation's own account")

	ctx.QueryConfig.DelegatedTarget = &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod", AccountID: "prod-id"}
	id, err = TargetDataAccountID(ctx)
	require.NoError(t, err)
	require.Equal(t, "prod-id", id)

	scoped, err := targetDataContext(ctx)
	require.NoError(t, err)
	require.Equal(t, "prod-id", scoped.AccountId)
	require.Nil(t, scoped.QueryConfig.DelegatedTarget)
	require.Equal(t, "origin-id", ctx.AccountId, "the caller's context, and so its workspace, is unchanged")

	ctx.QueryConfig.DelegatedTarget = &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod", AccountID: "replaced-id"}
	_, err = TargetDataAccountID(ctx)
	require.ErrorContains(t, err, "changed since handoff")

	ctx.Ctx = security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"origin-id"})
	ctx.QueryConfig.DelegatedTarget = &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod", AccountID: "prod-id"}
	_, err = TargetDataAccountID(ctx)
	require.Error(t, err, "a caller without access to the target must not read its data")
}

func TestRecommendationsFollowTheTarget(t *testing.T) {
	previous := TenantAccountLister
	TenantAccountLister = func(_ *security.RequestContext, tool core.NBTool) ([]core.ToolConfig, error) {
		if tool.Name() != ToolExecuteKubectlCommand {
			return nil, nil
		}
		return []core.ToolConfig{
			{Name: "k8s-prod", Values: []core.ToolConfigValue{{Name: "id", Value: "prod-id"}}},
			{Name: "k8s-stage", Values: []core.ToolConfigValue{{Name: "id", Value: "stage-id"}}},
		}, nil
	}
	t.Cleanup(func() { TenantAccountLister = previous })

	tool := RecommendationExecuteTool{}
	require.Contains(t, tool.InputSchema().Properties, "target")
	prepared, err := tool.PrepareDelegatedTargetInput(&core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod"}, "SELECT id FROM recommendation_view")
	require.NoError(t, err)
	require.JSONEq(t, `{"command":"SELECT id FROM recommendation_view","target":{"provider":"kubernetes","name":"k8s-prod"}}`, prepared, "the SQL stays the command")

	ctx := core.NbToolContext{
		Ctx:         security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"origin-id", "prod-id", "stage-id"}),
		AccountId:   "origin-id",
		QueryConfig: core.NBQueryConfig{DelegatedTarget: &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod", AccountID: "prod-id"}},
	}
	scoped, err := callDataContext(ctx, core.NBToolCallRequest{})
	require.NoError(t, err)
	require.Equal(t, "prod-id", scoped.AccountId)
	scoped, err = callDataContext(ctx, core.NBToolCallRequest{Arguments: map[string]any{"target": map[string]any{"provider": "kubernetes", "name": "k8s-stage"}}})
	require.NoError(t, err)
	require.Equal(t, "stage-id", scoped.AccountId, "an explicit per-call target wins")
	ctx.QueryConfig.DelegatedTarget = nil
	scoped, err = callDataContext(ctx, core.NBToolCallRequest{Arguments: map[string]any{"target": map[string]any{"provider": "kubernetes", "name": "k8s-prod"}}})
	require.NoError(t, err)
	require.Equal(t, "prod-id", scoped.AccountId, "a top-level agent can name the cluster per call")
	_, err = callDataContext(ctx, core.NBToolCallRequest{Arguments: map[string]any{"target": map[string]any{"provider": "kubernetes", "name": "k8s-secret"}}})
	require.Error(t, err)
}
