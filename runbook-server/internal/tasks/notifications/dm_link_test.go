package notifications

import (
	"testing"

	"nudgebee/runbook/config"
	"nudgebee/runbook/internal/tasks/testutils"

	"github.com/stretchr/testify/assert"
)

func TestBuildWorkflowDmLink(t *testing.T) {
	prev := config.Config.BaseUrl
	config.Config.BaseUrl = "https://app.example.com"
	t.Cleanup(func() { config.Config.BaseUrl = prev })

	withAccount := &testutils.MockTaskContext{WfID: "wf-1", Account: "acct-1"}
	assert.Equal(t, "<https://app.example.com/automation/wf-1?accountId=acct-1&utm=slack|*cleanup*>",
		buildWorkflowDmLink(withAccount, "cleanup"))

	noAccount := &testutils.MockTaskContext{WfID: "wf-1"}
	assert.Equal(t, "<https://app.example.com/automation/wf-1?utm=slack|*cleanup*>",
		buildWorkflowDmLink(noAccount, "cleanup"))

	assert.Equal(t, "", buildWorkflowDmLink(&testutils.MockTaskContext{}, "cleanup"))

	config.Config.BaseUrl = "https://app.example.com/"
	assert.Contains(t, buildWorkflowDmLink(withAccount, "cleanup"), "<https://app.example.com/automation/wf-1?")
	config.Config.BaseUrl = ""
	assert.Equal(t, "", buildWorkflowDmLink(withAccount, "cleanup"))
}
