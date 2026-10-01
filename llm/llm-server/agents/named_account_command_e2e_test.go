//go:build e2e

package agents

import (
	"os"
	"testing"

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
