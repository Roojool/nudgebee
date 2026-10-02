package tools

import (
	"testing"

	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
	"nudgebee/llm/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsTargetedIntegrationTool(t *testing.T) {
	assert.True(t, IsTargetedIntegrationTool(nil, PostgresExecuteTool{}))
	assert.True(t, IsTargetedIntegrationTool(nil, RedisExecuteTool{}))
	assert.True(t, IsTargetedIntegrationTool(nil, RabbitExecuteTool{}))
	assert.False(t, IsTargetedIntegrationTool(nil, KubectlExecuteTool{}), "picks its own cluster")
	assert.False(t, IsTargetedIntegrationTool(nil, PrometheusExecuteTool{}), "follows a target but has no integration to pick")
	assert.False(t, IsTargetedIntegrationTool(nil, nil))
}

func TestCallHasTarget(t *testing.T) {
	prod := &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod", AccountID: "prod-id"}
	assert.True(t, CallHasTarget(`{"query":"select 1","target":{"provider":"kubernetes","name":"k8s-prod"}}`, nil, "origin-id"))
	assert.True(t, CallHasTarget(`{"query":"select 1"}`, prod, "origin-id"), "a handed target")
	assert.False(t, CallHasTarget(`{"query":"select 1"}`, nil, "origin-id"))
	assert.False(t, CallHasTarget(`{"query":"select 1"}`, &core.ExecutionTarget{AccountID: "origin-id"}, "origin-id"), "handed the conversation's own account")
	assert.False(t, CallHasTarget(`select 1`, nil, "origin-id"), "plain text")
}

// The integration is picked among the target account's own: the only one, or
// the one named; never the conversation's, and never one of two with one name.
func TestTargetIntegrationConfig(t *testing.T) {
	byAccount := map[string][]core.ToolConfig{
		"prod-id": {{Id: "1", Name: "prod"}, {Id: "2", Name: "prod-pg"}},
		"one-id":  {{Id: "3", Name: "only-pg"}},
		"dup-id":  {{Id: "4", Name: "orders-db"}, {Id: "5", Name: "orders-db"}},
		"none-id": nil,
	}
	restore := listIntegrationConfigs
	t.Cleanup(func() { listIntegrationConfigs = restore })
	listIntegrationConfigs = func(_ *security.RequestContext, account string, _ core.NBTool) ([]core.ToolConfig, error) {
		return byAccount[account], nil
	}
	ctxOn := func(account string) core.NbToolContext {
		return core.NbToolContext{AccountId: "origin-id", TargetAccountId: account, ToolConfig: core.ToolConfig{Name: "dev-pg"},
			ExecutedTarget: &core.ExecutionTarget{Provider: "kubernetes", Name: account}}
	}
	pick := func(account, input string) (core.ToolConfig, error) {
		return targetIntegrationConfig(ctxOn(account), PostgresExecuteTool{}, core.NBToolCallRequest{Command: input})
	}

	got, err := pick("one-id", `{"query":"select 1"}`)
	require.NoError(t, err)
	assert.Equal(t, "only-pg", got.Name, "the only integration on the target, not the conversation's dev-pg")

	got, err = pick("prod-id", `{"query":"select 1","instance":"prod-pg"}`)
	require.NoError(t, err)
	assert.Equal(t, "2", got.Id)

	_, err = pick("prod-id", `{"query":"select 1"}`)
	assert.ErrorContains(t, err, "name one with `instance`")
	assert.ErrorContains(t, err, "prod, prod-pg")

	_, err = pick("dup-id", `{"query":"select 1","instance":"orders-db"}`)
	assert.ErrorContains(t, err, "cannot be told apart")

	_, err = pick("none-id", `{"query":"select 1"}`)
	assert.ErrorContains(t, err, "has no integration")

	_, err = IntegrationConfigByName(nil, "dup-id", PostgresExecuteTool{}, "orders-db")
	assert.ErrorContains(t, err, "none was used")
	got, err = IntegrationConfigByName(nil, "prod-id", PostgresExecuteTool{}, "PROD-PG")
	require.NoError(t, err)
	assert.Equal(t, "2", got.Id)
}

func TestIntegrationWorkspaceEnv(t *testing.T) {
	own := core.NbToolContext{AccountId: "origin-id", TargetAccountId: "origin-id", ToolConfig: core.ToolConfig{Name: "dev-pg"}}
	env := IntegrationWorkspaceEnv(own, map[string]string{"PGDATABASE": "app"})
	assert.Equal(t, map[string]string{workspace.ENV_NB_TOOL_CONFIG_NAME: "dev-pg", "PGDATABASE": "app"}, env, "no binding on the conversation's own account")

	other := core.NbToolContext{AccountId: "origin-id", TargetAccountId: "prod-id", ToolConfig: core.ToolConfig{Name: "prod-pg"}}
	env = IntegrationWorkspaceEnv(other, nil)
	assert.Equal(t, "prod-id", env[workspace.ENV_NB_TARGET_ACCOUNT_ID], "the command's token is bound to the target")
	assert.Equal(t, "prod-pg", env[workspace.ENV_NB_TOOL_CONFIG_NAME])
}

// The target's own name in the call must not pick an integration: k8s-prod
// has "prod" and "prod-pg", and naming neither picks neither.
func TestTargetIntegrationConfig_TargetNameDoesNotPick(t *testing.T) {
	restore := listIntegrationConfigs
	t.Cleanup(func() { listIntegrationConfigs = restore })
	listIntegrationConfigs = func(*security.RequestContext, string, core.NBTool) ([]core.ToolConfig, error) {
		return []core.ToolConfig{{Id: "1", Name: "prod"}, {Id: "2", Name: "prod-pg"}}, nil
	}
	input := `{"query":"select 1","target":{"provider":"kubernetes","name":"k8s-prod"}}`
	ctx := core.NbToolContext{AccountId: "origin-id", TargetAccountId: "prod-id", Query: input,
		ExecutedTarget: &core.ExecutionTarget{Provider: "kubernetes", Name: "k8s-prod"}}
	_, err := targetIntegrationConfig(ctx, PostgresExecuteTool{}, core.NBToolCallRequest{Command: input})
	assert.ErrorContains(t, err, "name one with `instance`")
}

// The user's pick among a target's integrations is used for that account only,
// and integrations sharing one name are refused with a rename hint.
func TestTargetIntegrationConfig_PickAndDuplicates(t *testing.T) {
	byAccount := map[string][]core.ToolConfig{
		"prod-id": {{Id: "1", Name: "prod"}, {Id: "2", Name: "prod-pg"}},
		"dup-id":  {{Id: "4", Name: "orders-db"}, {Id: "5", Name: "orders-db"}},
	}
	restore := listIntegrationConfigs
	t.Cleanup(func() { listIntegrationConfigs = restore })
	listIntegrationConfigs = func(_ *security.RequestContext, account string, _ core.NBTool) ([]core.ToolConfig, error) {
		return byAccount[account], nil
	}
	pick := func(account string, picks map[string]string) (core.ToolConfig, error) {
		ctx := core.NbToolContext{AccountId: "origin-id", TargetAccountId: account, QueryConfig: core.NBQueryConfig{ToolConfigs: picks},
			ExecutedTarget: &core.ExecutionTarget{Provider: "kubernetes", Name: account}}
		return targetIntegrationConfig(ctx, PostgresExecuteTool{}, core.NBToolCallRequest{Command: `{"query":"select 1"}`})
	}

	got, err := pick("prod-id", map[string]string{TargetIntegrationChoiceKey(ToolExecutePostgresQuery, "prod-id"): "prod-pg"})
	require.NoError(t, err)
	assert.Equal(t, "2", got.Id, "the pick the user made for k8s-prod")

	_, err = pick("prod-id", map[string]string{ToolExecutePostgresQuery: "prod-pg"})
	assert.ErrorContains(t, err, "name one with `instance`", "the conversation account's pick does not choose on another account")

	_, err = pick("dup-id", nil)
	assert.ErrorContains(t, err, "all named \"orders-db\"")
	assert.ErrorContains(t, err, "rename one of them in Integrations")

	assert.Equal(t, []string{"orders-db"}, UniqueConfigNames(byAccount["dup-id"]))
	assert.Equal(t, []string{"prod", "prod-pg"}, UniqueConfigNames(byAccount["prod-id"]))
}
