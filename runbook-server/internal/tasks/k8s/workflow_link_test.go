package k8s

import (
	"strings"
	"testing"

	"nudgebee/runbook/config"
	"nudgebee/runbook/internal/tasks/testutils"

	"github.com/stretchr/testify/assert"
)

// getWorkflowBaseLink must not fabricate a workflow link for isolated "Run Task"
// executions, which carry no workflow id / run id. A malformed
// "/automation/?accountId=...&executionId=" link would otherwise leak into GitOps
// PR bodies and Kubernetes annotations. Empty id => empty link so the callers'
// `if link != ""` guards actually skip it.
func TestGetWorkflowBaseLink_EmptyWorkflowIDReturnsEmpty(t *testing.T) {
	taskCtx := &testutils.MockTaskContext{
		Account:       "acct-1",
		WfID:          "", // isolated run — no real workflow execution
		WorkflowRunId: "",
	}

	assert.Equal(t, "", getWorkflowBaseLink(taskCtx))
}

func TestGetWorkflowBaseLink_PopulatedWorkflowIDReturnsLink(t *testing.T) {
	taskCtx := &testutils.MockTaskContext{
		Account:       "acct-1",
		WfID:          "wf-1",
		WorkflowRunId: "run-1",
	}

	link := getWorkflowBaseLink(taskCtx)
	assert.NotEqual(t, "", link)
	assert.True(t, strings.Contains(link, "/automation/wf-1"), "link should embed the workflow id: %s", link)
	assert.True(t, strings.Contains(link, "accountId=acct-1"), "link should embed the account id: %s", link)
	assert.True(t, strings.Contains(link, "executionId=run-1"), "link should embed the run id: %s", link)
}

func TestGetWorkflowBaseLink_TrimsSlashAndSkipsEmptyBaseURL(t *testing.T) {
	prev := config.Config.BaseUrl
	t.Cleanup(func() { config.Config.BaseUrl = prev })
	taskCtx := &testutils.MockTaskContext{Account: "acct-1", WfID: "wf-1", WorkflowRunId: "run-1"}

	config.Config.BaseUrl = "https://app.example.com/"
	assert.Equal(t, "https://app.example.com/automation/wf-1?accountId=acct-1&executionId=run-1", getWorkflowBaseLink(taskCtx))

	config.Config.BaseUrl = ""
	assert.Equal(t, "", getWorkflowBaseLink(taskCtx), "no base url must not produce a relative link")
}
