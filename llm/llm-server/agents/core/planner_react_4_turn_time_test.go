package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The human message sits right after the system prompt, so anything in it that
// changes between tool steps breaks provider prefix caching for the whole tool
// loop (#39633). Its timestamp must be the turn's start time, not the clock at
// each Plan() call.
func TestReact4HumanTextTimestampStableAcrossSteps(t *testing.T) {
	started := time.Date(2026, 9, 29, 18, 13, 41, 0, time.UTC)
	planner := &NBReActPlanner4{
		request:       NBAgentRequest{AgentId: "a", AccountId: "acc-1"},
		turnStartedAt: started,
	}

	first := planner.humanText("q")
	second := planner.humanText("q")

	assert.Contains(t, first, "Tuesday, September 29, 2026, 18:13:41 UTC")
	assert.Equal(t, first, second, "human message must be byte-identical across steps of one turn")
}

func TestReact4TurnStartTimeCapturedOnce(t *testing.T) {
	planner := &NBReActPlanner4{request: NBAgentRequest{AgentId: "a", AccountId: "acc-1"}}

	before := time.Now().UTC()
	got := planner.turnStartTime()

	assert.False(t, got.Before(before.Add(-time.Second)), "captured at first use")
	assert.Equal(t, time.UTC, got.Location())
	assert.Equal(t, got, planner.turnStartTime(), "later calls reuse the captured time")
}
