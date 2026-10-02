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
	case "vm":
		selected, err = VMAccountTargetByName(ctx, accountID, target.Name)
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

// ResolveCallTarget sets ctx.TargetAccountId for one tool call: the call's
// explicit "target" argument, else the target handed to the agent, else the
// conversation's own account. The caller must be able to read the target, and
// a handed target must still be the connection validated at handoff.
func ResolveCallTarget(ctx core.NbToolContext, input core.NBToolCallRequest) (core.NbToolContext, error) {
	target, accountID, err := ResolveObservabilityTarget(ctx, input)
	if err != nil {
		return ctx, err
	}
	ctx.TargetAccountId = accountID
	if target != nil && accountID != ctx.AccountId {
		ctx.ExecutedTarget = &core.ExecutionTarget{Provider: target.Provider, Name: target.Name, AccountID: accountID}
	}
	return ctx, nil
}

// ExecutedTargetOf reports where a tool call ran when that is not the
// conversation's default: the resolved target of a follows-target tool, the
// cluster/account an own-selector tool was asked for (it validates the name
// and fails otherwise), or the target an agent called as a tool was given.
func ExecutedTargetOf(tool core.NBTool, ctx core.NbToolContext, input string) *core.ExecutionTarget {
	if ctx.ExecutedTarget != nil {
		return ctx.ExecutedTarget
	}
	owner, requested := "", ""
	if strings.EqualFold(tool.Name(), core.ToolExecuteShellCommand) {
		owner, requested, _ = RequestedShellTargetFromInput(input)
	} else if scoped, ok := tool.(core.PerCallTargetTool); ok {
		owner = tool.Name()
		requested, _ = scoped.RequestedTarget(input)
	}
	provider := providerOfTool(owner)
	if requested == "" || provider == "" {
		// Agents and sub-agent tools (metrics, prometheus, delegate) take
		// their target in the input and resolve it themselves.
		target, err := core.ParseExecutionTarget(core.NBToolCallRequest{Command: input})
		if err != nil {
			return nil
		}
		return target
	}
	if strings.EqualFold(requested, ctx.ToolConfig.Name) {
		return nil // named the conversation's own connection
	}
	return &core.ExecutionTarget{Provider: provider, Name: requested}
}

// StampExecutedTarget records on a finished call where it ran, so the
// conversation view can show it. Only successful calls that ran somewhere
// other than the conversation's own account are stamped: a failed call (an
// unknown cluster, a refused target) did not run there.
func StampExecutedTarget(resp core.NBToolResponse, callErr error, tool core.NBTool, ctx core.NbToolContext, input string) core.NBToolResponse {
	if !ranToCompletion(resp, callErr) {
		return resp
	}
	if _, recorded := resp.AdditionalDetails[core.MetadataExecutedTarget]; recorded {
		return resp
	}
	target := ExecutedTargetOf(tool, ctx, input)
	if target == nil {
		return resp
	}
	if target.AccountID == "" {
		// An own-selector tool names a connection; the conversation's own
		// cluster may be named even when its tool config carries no name.
		resolved, err := resolveStampTarget(ctx.Ctx, ctx.AccountId, target)
		if err != nil {
			return resp
		}
		target = resolved
	}
	if target.AccountID == ctx.AccountId {
		return resp
	}
	resp.AdditionalDetails = core.WithExecutedTarget(resp.AdditionalDetails, target)
	return resp
}

// resolveStampTarget is replaced in tests, which have no connection store.
var resolveStampTarget = ResolveAgentHandoffTarget

// ranToCompletion: a failed call did not run there, and a call waiting on the
// user has not run yet.
func ranToCompletion(resp core.NBToolResponse, callErr error) bool {
	return callErr == nil && resp.Status != core.NBToolResponseStatusError && resp.Status != core.NBToolResponseStatusWaiting
}

func providerOfTool(name string) string {
	switch name {
	case ToolExecuteKubectlCommand, ToolExecuteHelmCommand, ToolResourceSearch:
		return "kubernetes"
	case ToolExecuteAwsCliCommand:
		return "aws"
	case ToolExecuteGcpCliCommand:
		return "gcp"
	case ToolExecuteAzureCliCommand:
		return "azure"
	}
	return ""
}

// ResolveToolCallTarget decides, in one place, which account a tool call
// reads. Every dispatch path uses it: the planner executor, custom planners
// (core.CallTool) and watch polls.
//
// A follows-target tool gets TargetAccountId from the call's explicit target,
// else the target handed to the agent, after the caller's access to it is
// checked. Agent-tools and own-selector tools route a target themselves. Any
// other tool given a target argument it does not define is refused rather
// than silently answering for the conversation's own account.
func ResolveToolCallTarget(ctx core.NbToolContext, tool core.NBTool, request core.NBToolCallRequest) (core.NbToolContext, error) {
	if _, agentTool := tool.(core.DelegatedTargetSupport); agentTool {
		return ctx, nil
	}
	if _, ownSelector := tool.(core.DelegatedTargetInputPreparer); ownSelector {
		return ctx, nil
	}
	decl, _ := core.AccountScopeOf(tool.Name())
	switch decl.Scope {
	case core.AccountScopeFollowsTarget:
		resolved, err := ResolveCallTarget(ctx, request)
		if err != nil || !IsTargetedIntegrationTool(ctx.Ctx, tool) {
			return resolved, err
		}
		// The conversation's own integration was picked when the context was
		// built; a call on another account, or one that named its target
		// without a default, picks among the target's integrations instead.
		if resolved.TargetAccountId == resolved.AccountId && resolved.ToolConfig.Name != "" {
			return resolved, nil
		}
		selected, err := targetIntegrationConfig(resolved, tool, request)
		if err != nil {
			return ctx, err
		}
		resolved.ToolConfig = selected
		return resolved, nil
	case core.AccountScopeIndependent:
		return ctx, nil
	}
	if raw, given := request.Arguments["target"]; given && raw != nil {
		if _, own := tool.InputSchema().Properties["target"]; !own {
			return ctx, fmt.Errorf("%s cannot run on another cluster or account: it only covers this conversation's own account", tool.Name())
		}
	}
	return ctx, nil
}
