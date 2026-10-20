package event

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetPlaybookStartEndTime_ReturnsAlertStartBesideTheLeadIn(t *testing.T) {
	alert := time.Date(2026, 9, 25, 10, 55, 52, 0, time.UTC)

	start, end, detected := getPlaybookStartEndTime(Event{StartsAt: &alert})

	assert.Equal(t, alert.Add(-10*time.Minute), start, "window keeps its 10-minute lead-in")
	assert.Equal(t, alert.Add(time.Hour), end)
	assert.Equal(t, alert, detected, "detected is the alert start, not the window start")
}

func TestGetPlaybookStartEndTime_AlertStartFromLabels(t *testing.T) {
	start, _, detected := getPlaybookStartEndTime(Event{
		Labels: map[string]string{"starts_at": "2026-09-25T11:10:30Z"},
	})

	alert := time.Date(2026, 9, 25, 11, 10, 30, 0, time.UTC)
	assert.Equal(t, alert, detected)
	assert.Equal(t, alert.Add(-10*time.Minute), start)
}
