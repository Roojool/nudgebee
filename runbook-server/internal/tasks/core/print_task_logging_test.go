package core

import (
	"testing"

	"nudgebee/runbook/internal/logsafe"

	"github.com/stretchr/testify/assert"
)

// print renders whatever it is given, which can be a resolved credential. The
// log line must describe it, not repeat it.
func TestPrintTask_LogsShapeNotValue(t *testing.T) {
	const canary = "Zx9-canary-7Qw-live"
	assert.NotContains(t, logsafe.Shape("value is "+canary), canary)
	assert.Equal(t, "<string: 28 chars>", logsafe.Shape("value is "+canary))
}
