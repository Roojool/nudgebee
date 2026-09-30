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

func stubTargets(t *testing.T) {
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
}

// ResolveCallTarget is the one place a call's data account is decided.
func TestResolveCallTarget(t *testing.T) {
	stubTargets(t)
	sc := security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"origin-id", "prod-id", "stage-id"})
	ctx := core.NbToolContext{Ctx: sc, AccountId: "origin-id"}
	prodTarget := map[string]any{"target": map[string]any{"provider": "kubernetes", "name": "k8s-prod"}}

	got, err := ResolveCallTarget(ctx, core.NBToolCallRequest{})
	require.NoError(t, err)
	require.Equal(t, "origin-id", got.DataAccountID(), "no target: the conversation's own account")

	got, err = ResolveCallTarget(ctx, core.NBToolCallRequest{Arguments: prodTarget})
	require.NoError(t, err)
	require.Equal(t, "prod-id", got.DataAccountID(), "a top-level agent can name the cluster per call")
	require.Equal(t, "origin-id", got.AccountId, "the conversation's account is unchanged")

	ctx.QueryConfig.DelegatedTarget = &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-stage", AccountID: "stage-id"}
	got, err = ResolveCallTarget(ctx, core.NBToolCallRequest{})
	require.NoError(t, err)
	require.Equal(t, "stage-id", got.DataAccountID(), "a handed target applies by default")
	got, err = ResolveCallTarget(ctx, core.NBToolCallRequest{Arguments: prodTarget})
	require.NoError(t, err)
	require.Equal(t, "prod-id", got.DataAccountID(), "an explicit target wins over the handed one")

	ctx.QueryConfig.DelegatedTarget = &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod", AccountID: "replaced-id"}
	_, err = ResolveCallTarget(ctx, core.NBToolCallRequest{})
	require.ErrorContains(t, err, "changed since handoff")

	ctx = core.NbToolContext{Ctx: security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"origin-id"}), AccountId: "origin-id"}
	_, err = ResolveCallTarget(ctx, core.NBToolCallRequest{Arguments: prodTarget})
	require.Error(t, err, "a caller without access to the target must not read its data")
}

func TestForTargetKeepsTheOriginalContextForTheConversation(t *testing.T) {
	ctx := core.NbToolContext{AccountId: "origin-id", TargetAccountId: "prod-id", QueryConfig: core.NBQueryConfig{DelegatedTarget: &core.ExecutionTarget{Name: "k8s-prod"}}}
	data := ctx.ForTarget()
	require.Equal(t, "prod-id", data.AccountId)
	require.Nil(t, data.QueryConfig.DelegatedTarget)
	require.Equal(t, "origin-id", ctx.AccountId, "the caller's context, and so its workspace, is unchanged")
}

func TestModelInputSchemaAddsTargetOnce(t *testing.T) {
	require.Contains(t, core.ModelInputSchema(RecommendationExecuteTool{}).Properties, "target", "follows-target tools get the input")
	require.NotContains(t, RecommendationExecuteTool{}.InputSchema().Properties, "target", "tools do not declare it themselves")
	require.NotContains(t, core.ModelInputSchema(KubectlExecuteTool{}).Properties, "target", "own-selector tools keep their own argument")
	require.NotContains(t, core.ModelInputSchema(DatadogLogExecuteTool{}).Properties, "target", "origin-bound tools cannot take one")
}

type ownTargetTool struct{ DatadogLogExecuteTool }

func (ownTargetTool) Name() string { return "custom_with_own_target" }
func (ownTargetTool) InputSchema() core.ToolSchema {
	return core.ToolSchema{Properties: map[string]core.ToolSchemaProperty{"target": {Type: core.ToolSchemaTypeString}}}
}

// Every dispatch path (executor, custom planners, watch polls) decides the
// call's account through ResolveToolCallTarget.
func TestResolveToolCallTarget(t *testing.T) {
	stubTargets(t)
	ctx := core.NbToolContext{Ctx: security.NewRequestContextForTenantAccountAdmin("tenant", "user", []string{"origin-id", "prod-id"}), AccountId: "origin-id"}
	prod := core.NBToolCallRequest{Arguments: map[string]any{"target": map[string]any{"provider": "kubernetes", "name": "k8s-prod"}}}

	got, err := ResolveToolCallTarget(ctx, RecommendationExecuteTool{}, prod)
	require.NoError(t, err)
	require.Equal(t, "prod-id", got.DataAccountID(), "a follows-target tool reads the named account")

	_, err = ResolveToolCallTarget(ctx, DatadogLogExecuteTool{}, prod)
	require.ErrorContains(t, err, "cannot run on another cluster or account", "an origin-bound tool refuses a target")
	_, err = ResolveToolCallTarget(ctx, DatadogLogExecuteTool{}, core.NBToolCallRequest{Arguments: map[string]any{"target": "k8s-prod"}})
	require.Error(t, err, "a malformed target is refused too, not ignored")

	got, err = ResolveToolCallTarget(ctx, KubectlExecuteTool{}, prod)
	require.NoError(t, err)
	require.Equal(t, "origin-id", got.DataAccountID(), "own-selector tools route the target themselves")

	_, err = ResolveToolCallTarget(ctx, ownTargetTool{}, core.NBToolCallRequest{Arguments: map[string]any{"target": "anything"}})
	require.NoError(t, err, "a tool that defines its own target argument is left alone")

	got, err = ResolveToolCallTarget(ctx, DatadogLogExecuteTool{}, core.NBToolCallRequest{Command: `{"target":{"provider":"aws","name":"x"}}`})
	require.NoError(t, err, "a target key inside the command payload is not a target argument")
	require.Equal(t, "origin-id", got.DataAccountID())
}
