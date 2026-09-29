package tools

import (
	"fmt"
	"strings"

	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
)

// ResolveAgentHandoffTarget validates a child agent's named connection. The
// execution tool still resolves and authorizes its concrete call separately.
func ResolveAgentHandoffTarget(ctx *security.RequestContext, accountID string, target *core.ExecutionTarget) (*core.ExecutionTarget, error) {
	if target == nil {
		return nil, nil
	}
	if ctx == nil || ctx.GetSecurityContext() == nil {
		return nil, fmt.Errorf("caller security context is required")
	}
	var selected core.ToolConfig
	var err error
	switch target.Provider {
	case "kubernetes":
		selected, err = KubernetesTargetByName(ctx, accountID, target.Name)
	case "aws":
		selected, err = AccountTargetByName(ctx, accountID, ToolExecuteAwsCliCommand, target.Name)
	case "gcp":
		selected, err = AccountTargetByName(ctx, accountID, ToolExecuteGcpCliCommand, target.Name)
	case "azure":
		selected, err = AccountTargetByName(ctx, accountID, ToolExecuteAzureCliCommand, target.Name)
	default:
		return nil, fmt.Errorf("unsupported provider %q", target.Provider)
	}
	if err != nil {
		return nil, err
	}
	targetAccountID := TargetAccountID(selected)
	if targetAccountID == "" {
		return nil, fmt.Errorf("%s target %q has no account id", target.Provider, selected.Name)
	}
	return &core.ExecutionTarget{Provider: target.Provider, Name: selected.Name, AccountID: targetAccountID}, nil
}

// VerifyDelegatedTargetIdentity prevents a renamed/replaced connection from
// changing where a child runs after its target was validated or approved.
func VerifyDelegatedTargetIdentity(ctx core.NbToolContext, provider string, selected core.ToolConfig, requested string) error {
	bound := ctx.QueryConfig.DelegatedTarget
	if bound == nil || bound.AccountID == "" || bound.Provider != provider ||
		(!strings.EqualFold(requested, bound.Name) && !strings.EqualFold(selected.Name, bound.Name)) {
		return nil
	}
	if actual := TargetAccountID(selected); actual != bound.AccountID {
		return fmt.Errorf("delegated %s target %q changed since handoff; select the connection again", provider, bound.Name)
	}
	return nil
}

// ResolveObservabilityTarget resolves and validates a target for an observability tool
// (metrics, traces, logs). If an explicit target is provided in input, it resolves and
// verifies that target. Otherwise, if ctx.QueryConfig.DelegatedTarget is bound, it
// uses that bound target. If no target is present, it returns nil, ctx.AccountId, nil.
func ResolveObservabilityTarget(ctx core.NbToolContext, input core.NBToolCallRequest) (*core.ExecutionTarget, string, error) {
	target, err := core.ParseExecutionTarget(input)
	if err != nil {
		return nil, "", fmt.Errorf("target: %w", err)
	}
	if target == nil {
		target = ctx.QueryConfig.DelegatedTarget
	}
	if target == nil {
		return nil, ctx.AccountId, nil
	}
	if ctx.Ctx == nil && target.AccountID == "" {
		return nil, "", fmt.Errorf("observability target %s/%s: caller security context is required to resolve target", target.Provider, target.Name)
	}
	// If the target already has an AccountID and caller request context is nil (common in unit tests),
	// use it directly.
	if target.AccountID != "" && ctx.Ctx == nil {
		return target, target.AccountID, nil
	}
	resolved, err := ResolveAgentHandoffTarget(ctx.Ctx, ctx.AccountId, target)
	if err != nil {
		return nil, "", fmt.Errorf("observability target %s/%s: %w", target.Provider, target.Name, err)
	}
	if target.AccountID != "" && resolved.AccountID != target.AccountID {
		return nil, "", fmt.Errorf("delegated %s target %q changed since handoff; select the connection again", target.Provider, target.Name)
	}
	return resolved, resolved.AccountID, nil
}

// TargetDataAccountID is the account a data query should read: the delegated
// target's, re-validated for this caller, or the conversation's own account.
// Workspace files stay on ctx.AccountId, which the parent agent can read.
func TargetDataAccountID(ctx core.NbToolContext) (string, error) {
	if ctx.QueryConfig.DelegatedTarget == nil {
		return ctx.AccountId, nil
	}
	_, accountID, err := ResolveObservabilityTarget(ctx, core.NBToolCallRequest{})
	return accountID, err
}

// targetDataContext returns ctx scoped to the account its data queries must
// read. Only for tools that write nothing to the workspace: the copy's
// AccountId is the target's.
func targetDataContext(ctx core.NbToolContext) (core.NbToolContext, error) {
	return callDataContext(ctx, core.NBToolCallRequest{})
}

// callDataContext is targetDataContext for tools that also take a per-call
// "target" argument: an explicit target wins over the delegated one, and both
// are validated for this caller.
func callDataContext(ctx core.NbToolContext, input core.NBToolCallRequest) (core.NbToolContext, error) {
	_, accountID, err := ResolveObservabilityTarget(ctx, input)
	if err != nil {
		return ctx, err
	}
	ctx.AccountId = accountID
	ctx.QueryConfig.DelegatedTarget = nil
	return ctx, nil
}
