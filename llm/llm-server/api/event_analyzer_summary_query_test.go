package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The step-1 summary is the first thing an on-call engineer reads, and it is also
// fed verbatim into the investigation prompt and the detailed-response synthesis.
// It regressed once into a triage-mechanics report (~77% of the body was score-factor
// arithmetic, dedup bookkeeping and unrelated co-firing alerts before the reader
// learned what was broken), so the contract is pinned here: still call the triage
// tools, never narrate the triage internals.
func TestBuildEventSummaryQuery_OnCallContract(t *testing.T) {
	q := buildEventSummaryQuery("aa3f7199-b608-40dc-80a3-61c918b64e0a")

	assert.Contains(t, q, "aa3f7199-b608-40dc-80a3-61c918b64e0a", "query must name the event id")

	// The triage tools still have to be exercised — step 1 is the only stage of the
	// automatic pipeline that runs the events agent.
	for _, tool := range []string{"get_event_triage_explanation", "get_event_incident_assembly"} {
		assert.Contains(t, q, tool, "query must still exercise %s", tool)
	}

	// ...but not as a narrated triage report.
	for _, banned := range []string{
		"explain how Nudgebee auto-triaged",
		"the score_factors that produced it",
	} {
		assert.NotContains(t, q, banned, "query must not ask the agent to narrate triage internals")
	}

	// The rendered card was inconsistent across runs (numbered bold list items one
	// run, H3 headings the next), so the section headings are pinned verbatim.
	for _, heading := range []string{
		"### What is broken",
		"### Since when",
		"### What changed just before",
		"### What else is firing",
		"### Priority",
	} {
		assert.Contains(t, q, heading, "query must pin the %q section", heading)
	}

	// Correlated signals are what on-call wants after "what changed" — the cause
	// tiers alone dropped the confirmed same-incident alerts.
	for _, tier := range []string{"same_incident", "impact", "cause.config_changes", "cause.upstream"} {
		assert.Contains(t, q, tier, "query must ask for the %s assembly tier", tier)
	}
	assert.Contains(t, q, "SKIP the chronic tier", "query must exclude background-noise correlations")

	lower := strings.ToLower(q)
	assert.Contains(t, lower, "on-call brief", "query must frame the output for an on-call reader")
	assert.Contains(t, lower, "300 words", "query must cap the brief's length")
}
