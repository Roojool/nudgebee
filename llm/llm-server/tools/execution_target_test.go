package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
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
