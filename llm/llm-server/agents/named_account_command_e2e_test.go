//go:build e2e

package agents

import (
	"fmt"
	"github.com/google/uuid"
	"nudgebee/llm/agents/core"
	"nudgebee/llm/common"
	"os"
	"strings"
	"testing"
	"time"

	"nudgebee/llm/tools"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNaturalQuestion_CommandInNamedAwsAccount (#39826): from a k8s cluster
// conversation, which has no AWS config of its own, a command in a named AWS
// account must run there instead of being refused as unconfigured.
func TestNaturalQuestion_CommandInNamedAwsAccount(t *testing.T) {
	requireDelegateE2EEnv(t)
	target := delegateE2ETarget("TEST_DELEGATE_AWS_TARGET", "aws-prod")
	selected, err := tools.AccountTargetByName(tenantWideE2EContext(), os.Getenv("TEST_ACCOUNT"), tools.ToolExecuteAwsCliCommand, target)
	require.NoError(t, err, "%s must be a connected AWS account in this tenant", target)
	expected := ""
	for _, v := range selected.Values {
		if v.Name == "account_number" {
			expected = v.Value
		}
	}
	require.NotEmpty(t, expected)

	resp := runPreservedE2EConversation(t, tenantWideE2EContext(), newK8sOrchestratorAgent(os.Getenv("TEST_ACCOUNT")), "named-aws-command",
		"Run aws sts get-caller-identity in the "+target+" AWS account and tell me its account number.")
	calls := fetchConversationToolCalls(t, resp.ConversationId)
	logRoute(t, calls)
	var ranThere int
	for _, c := range calls {
		assert.NotContains(t, c.Response, "requires configuration", "%s was refused as unconfigured: %s", c.ToolName, c.Parameters)
		if isRemoteCall(c, "aws") && ranOn(c, target) && c.Status == "success" {
			ranThere++
		}
	}
	assert.NotZero(t, ranThere, "an aws command must run in %s", target)
	assert.Contains(t, conversationAnswer(t, resp.ConversationId), expected, "the answer must give %s's account number", target)
}

// TestNaturalQuestion_VMsInAnotherAccount (#39894): from a k8s cluster
// conversation, a question about the VMs of a connected VM account must reach
// the VM tools on that account, not the conversation's own cluster.
func TestNaturalQuestion_VMsInAnotherAccount(t *testing.T) {
	requireDelegateE2EEnv(t)
	target := delegateE2ETarget("TEST_DELEGATE_VM_TARGET", "vulnerable-vms-test")
	resp := runPreservedE2EConversation(t, tenantWideE2EContext(), newK8sOrchestratorAgent(os.Getenv("TEST_ACCOUNT")), "natural-vms-other-account",
		"List the VMs in the "+target+" VM account and run uptime on one of them.")
	calls := fetchConversationToolCalls(t, resp.ConversationId)
	logRoute(t, calls)
	var onTarget int
	for _, c := range calls {
		if (c.ToolName == tools.ToolVMList || c.ToolName == tools.ToolVMExecute) && c.Status == "success" {
			onTarget++
		}
	}
	assert.NotZero(t, onTarget, "a VM tool must run on %s", target)
	assert.Contains(t, conversationAnswer(t, resp.ConversationId), "load average", "the answer must include uptime from a VM in %s", target)
}

// TestNaturalQuestion_PickTargetIntegration (#39906): asked about "the postgres
// database" on an account with several, Nubi asks the user which one, as it
// does for the conversation's own account, and runs the query on the pick.
func TestNaturalQuestion_PickTargetIntegration(t *testing.T) {
	requireDelegateE2EEnv(t)
	sc := tenantWideE2EContext()
	accountId, userId := os.Getenv("TEST_ACCOUNT"), os.Getenv("TEST_USER")
	sessionId := fmt.Sprintf("pick-target-integration-%d", time.Now().Unix())
	resp1, err := core.HandleConversationSessionRequest(sc, newK8sOrchestratorAgent(accountId), userId, accountId, sessionId,
		"On k8s-prod, run select current_database(), inet_server_addr() on postgres database.")
	require.NoError(t, err)
	t.Logf("Preserved sessionId=%s conversationId=%s status=%s question=%q options=%v", sessionId, resp1.ConversationId, resp1.Status, resp1.FollowupRequest.Question, resp1.FollowupRequest.FollowupOptions)
	require.Equal(t, core.ConversationStatusWaiting, resp1.Status, "Nubi must ask which k8s-prod database, not guess")
	assert.ElementsMatch(t, []string{"prod", "prod-pg"}, resp1.FollowupRequest.FollowupOptions, "the choices are k8s-prod's integrations")

	waiting := resp1.FollowupRequest.AgentId
	resolvedAgent, _, _, err := core.ResolveAgentByConversationAgentId(sc, waiting, accountId)
	require.NoError(t, err)
	_, err = core.HandleConversationSessionRequest(sc, resolvedAgent, userId, accountId, sessionId, "prod",
		core.ConversationSessionRequestWithConversationId(uuid.NullUUID{UUID: uuid.MustParse(resp1.ConversationId), Valid: true}),
		core.ConversationSessionRequestWithMessageId(uuid.NullUUID{UUID: uuid.MustParse(resp1.MessageId), Valid: true}),
		core.ConversationSessionRequestWithAgentId(uuid.NullUUID{UUID: waiting, Valid: true}),
		core.ConversationSessionRequestWithIsResume(true),
	)
	require.NoError(t, err)
	final := waitForTerminalMessageStatus(t, accountId, resp1.ConversationId, resp1.MessageId, 240*time.Second)
	t.Logf("final status after picking prod: %s", final)

	calls := fetchConversationToolCalls(t, resp1.ConversationId)
	logRoute(t, calls)
	var onPick int
	for _, c := range calls {
		if c.ToolName == tools.ToolExecutePostgresQuery && c.Status == "success" && strings.Contains(c.Response, "172.31.4.191") {
			onPick++
		}
	}
	assert.NotZero(t, onPick, "the query must run on k8s-prod's prod database after the pick")
}

// TestNaturalQuestion_StepsSayWhereTheyRan (#38588): metrics and logs about
// another cluster run in sub-agents and tools under that cluster's account.
// Every such step, succeeded or failed, says which account it ran on unless it
// named the conversation's own cluster, and the integration it names is one it
// ran with.
func TestNaturalQuestion_StepsSayWhereTheyRan(t *testing.T) {
	requireDelegateE2EEnv(t)
	target := delegateE2ETarget("TEST_DELEGATE_K8S_TARGET", "k8s-prod")
	resp := runPreservedE2EConversation(t, tenantWideE2EContext(), newK8sOrchestratorAgent(os.Getenv("TEST_ACCOUNT")), "steps-say-where-they-ran",
		"On "+target+", how much CPU did the pods in kube-system use over the last hour, and were there any error logs from them?")

	dbm, err := common.GetDatabaseManager(common.Metastore)
	require.NoError(t, err)
	rows, err := dbm.Db.Query(`
		SELECT t.tool_name, COALESCE(t.status, ''),
		       -- Run under the target's account, and not aimed back at the conversation's own cluster.
		       t.account_id::text <> c.account_id::text
		         AND position(lower(own.account_name) in lower(COALESCE(NULLIF(t.params_sql, ''), t.parameters, ''))) = 0,
		       COALESCE(t.metadata->'nb_executed_target'->>'name', ''), COALESCE(t.metadata->>'nb_resolved_target', '')
		FROM llm_conversation_tool_calls t JOIN llm_conversations c ON c.id = t.conversation_id
		JOIN cloud_accounts own ON own.id::text = c.account_id::text
		WHERE t.conversation_id = $1 AND COALESCE(t.status, '') NOT IN ('in_progress', 'waiting')`, resp.ConversationId)
	require.NoError(t, err)
	defer rows.Close()
	var elsewhere int
	for rows.Next() {
		var tool, status, ranOn, integration string
		var underOther bool
		require.NoError(t, rows.Scan(&tool, &status, &underOther, &ranOn, &integration))
		t.Logf("%s status=%s under_other_account=%v ran_on=%q integration=%q", tool, status, underOther, ranOn, integration)
		assert.NotContains(t, []string{"NoConfigToolsFound", "Agent"}, integration, "%s names a placeholder as its integration", tool)
		if underOther {
			elsewhere++
			assert.Equal(t, target, ranOn, "%s ran under %s's account but does not say so", tool, target)
		}
	}
	require.NoError(t, rows.Err())
	assert.NotZero(t, elsewhere, "metrics or logs for %s run under its account", target)
}
