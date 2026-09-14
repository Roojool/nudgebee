package agents

import (
	"testing"

	"nudgebee/code-analysis-agent/common"

	"github.com/stretchr/testify/assert"
)

// newTestOrchestrator builds a minimal OrchestratorAgent with just a logger — enough
// to exercise the pure decision helpers (shouldCreatePR, buildFailureSummary) without
// an LLM client or git access.
func newTestOrchestrator() *OrchestratorAgent {
	return &OrchestratorAgent{logger: common.NewLogger("test-analysis", "test/repo", "test-user", nil)}
}

func TestShouldCreatePR_EmptyDiffHonorsNoOpVerdict(t *testing.T) {
	a := newTestOrchestrator()

	// The fix loop classified this empty diff as a genuine no-op (the fixer
	// edited and the tree already matched), so shouldCreatePR must carry it
	// through as one.
	shouldCreate, noChanges, reason := a.shouldCreatePR(map[string]any{
		"execution_status":  ExecutionStatusNoOp,
		"execution_summary": "The fixer applied its edits to values.yaml and the working tree was left unchanged — the requested change is already present.",
	})

	assert.False(t, shouldCreate, "no PR should be created when there is no diff")
	assert.True(t, noChanges, "a classified no-op must stay a terminal no-op")
	assert.Contains(t, reason, "already present")
}

func TestShouldCreatePR_EmptyDiffFromFailedFixerIsNotNoOp(t *testing.T) {
	a := newTestOrchestrator()

	// The fix loop found no edit tool was ever invoked and marked the run failed.
	// shouldCreatePR must NOT relabel that as "already present" — doing so closes
	// a live recommendation with the target file untouched.
	shouldCreate, noChanges, reason := a.shouldCreatePR(map[string]any{
		"execution_status":  ExecutionStatusFailed,
		"execution_summary": "No change was applied to deploy/values-test.yaml: the fixer finished without invoking any file-editing tool, so nothing was written.",
	})

	assert.False(t, shouldCreate)
	assert.False(t, noChanges, "a fixer that never edited must not be reported as a no-op")
	assert.Contains(t, reason, "without invoking any file-editing tool")
	assert.NotContains(t, reason, "already present")
}

func TestShouldCreatePR_ReviewRejectedIsNotNoOp(t *testing.T) {
	a := newTestOrchestrator()

	shouldCreate, noChanges, reason := a.shouldCreatePR(map[string]any{
		"git_diff": "diff --git a/x b/x\n+change",
		"review": map[string]any{
			"approved": false,
			"feedback": "needs work",
		},
	})

	assert.False(t, shouldCreate)
	assert.False(t, noChanges, "a review rejection is a failure, not a no-op")
	assert.Contains(t, reason, "not approved")
}

func TestShouldCreatePR_DiffAndApprovalCreatesPR(t *testing.T) {
	a := newTestOrchestrator()

	shouldCreate, noChanges, _ := a.shouldCreatePR(map[string]any{
		"git_diff": "diff --git a/x b/x\n+change",
	})

	assert.True(t, shouldCreate)
	assert.False(t, noChanges)
}

func TestBuildFailureSummary_NoOpHasNoFailureSummary(t *testing.T) {
	a := newTestOrchestrator()

	// A no-op skip (change already present) must not read as a failure.
	summary := a.buildFailureSummary(map[string]any{
		"execution_status":   ExecutionStatusNoOp,
		"pr_creation_status": "skipped",
		"pr_creation_reason": "No code changes were produced — the requested change appears to already be present.",
		"requires_fix":       true,
	})

	assert.Empty(t, summary, "no-op is a success and must not produce a failure summary")
}

func TestBuildFailureSummary_RealFailureStillSummarized(t *testing.T) {
	a := newTestOrchestrator()

	summary := a.buildFailureSummary(map[string]any{
		"execution_status":  "failed",
		"execution_summary": "rejected by review",
	})

	assert.NotEmpty(t, summary, "a genuine failure must still be summarized")
}
