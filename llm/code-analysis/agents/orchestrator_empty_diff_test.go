package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmptyDiffFailureSummary_NeverClaimsTheChangeIsPresent(t *testing.T) {
	// The fix flow reaches the fixer only when the specialist read the file and
	// said a change is needed, so an empty diff here is the fix not landing.
	// Calling it "already present" is what closed a live recommendation with the
	// target file untouched.
	summary := emptyDiffFailureSummary("deploy/kubernetes/notifications/values-test.yaml", "success")

	assert.NotContains(t, summary, "already present")
	assert.Contains(t, summary, "deploy/kubernetes/notifications/values-test.yaml",
		"the summary must name the file nothing was written to")
	assert.Contains(t, summary, "did not happen",
		"the fixer's own claim of edits must be contradicted, not echoed")
}

func TestEmptyDiffFailureSummary_RecordsTheFixersSelfReport(t *testing.T) {
	summary := emptyDiffFailureSummary("values.yaml", "failed")

	assert.Contains(t, summary, `"failed"`, "the fixer's self-reported status is context worth keeping")
}

func TestEmptyDiffFailureSummary_ReadableWithoutAFilePathOrStatus(t *testing.T) {
	summary := emptyDiffFailureSummary("", "")

	assert.Contains(t, summary, "the target file")
	assert.NotContains(t, summary, `""`, "an absent status must not render as empty quotes")
}
