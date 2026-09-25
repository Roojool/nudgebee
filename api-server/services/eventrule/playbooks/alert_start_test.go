package playbooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAlertStart_PrefersDetectedAt(t *testing.T) {
	alert := time.Date(2026, 9, 25, 10, 55, 52, 0, time.UTC)
	windowStart := alert.Add(-10 * time.Minute)

	assert.Equal(t, alert, *PlaybookEvent{StartedAt: &windowStart, DetectedAt: &alert}.AlertStart())
	assert.Equal(t, windowStart, *PlaybookEvent{StartedAt: &windowStart}.AlertStart(), "hand-built events set only StartedAt")
	var zero time.Time
	assert.Equal(t, windowStart, *PlaybookEvent{StartedAt: &windowStart, DetectedAt: &zero}.AlertStart())
	assert.Nil(t, PlaybookEvent{}.AlertStart())
}

// The deploy-then-crash case from #38086: the command change landed 41s before
// the crash-loop alert. The history window must include it.
func TestHistoryAnchor_IncludesChangeJustBeforeAlert(t *testing.T) {
	alert := time.Date(2026, 9, 25, 10, 55, 52, 0, time.UTC)
	windowStart := alert.Add(-10 * time.Minute) // as the event service builds it
	ev := PlaybookEvent{StartedAt: &windowStart, DetectedAt: &alert}
	change := alert.Add(-41 * time.Second)

	anchor := historyAnchor(ev, time.Now())

	assert.Equal(t, alert, anchor)
	assert.False(t, change.After(anchor), "a change before the alert is inside the window")
	assert.True(t, change.After(windowStart), "anchoring on StartedAt is what dropped it")
	assert.Equal(t, "41 seconds", formatDuration(anchor.Sub(change)))
}

func TestHistoryAnchor_NoTimesFallsBackToNow(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, now, historyAnchor(PlaybookEvent{}, now))
}
