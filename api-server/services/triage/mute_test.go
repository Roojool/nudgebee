package triage

import (
	"testing"

	"nudgebee/services/eventrule"
	"nudgebee/services/internal/database/models"

	"github.com/stretchr/testify/assert"
)

// match_alertname is an unanchored regex over aggregation_key. The mute pattern
// must match the muted alarm exactly, even when its name is full of regex
// metacharacters, and never a longer alarm that shares its prefix.
func TestMuteAlertnamePattern_MatchesOnlyThatAlert(t *testing.T) {
	alert := `API (5xx) > 1% [prod] | p99+ $cost.*`
	pattern := MuteAlertnamePattern(alert)
	source := "AWS_CloudWatch_Alarm"
	rule := &TriageRule{RuleType: RuleTypeSuppression, MatchSource: &source, MatchAlertname: &pattern}

	event := func(key, src string) *models.Event {
		return &models.Event{AggregationKey: &key, Source: &src}
	}

	assert.True(t, MatchesRule(event(alert, source), rule))
	assert.False(t, MatchesRule(event(alert+" staging", source), rule), "longer same-prefix alarm")
	assert.False(t, MatchesRule(event("x"+alert, source), rule), "same-suffix alarm")
	assert.False(t, MatchesRule(event("API 5xx 1 prod", source), rule), "metacharacters must be literal")
	assert.False(t, MatchesRule(event(alert, "AWS_EventBridge"), rule), "one rule per source")
}

func TestMuteActionValue(t *testing.T) {
	v, err := muteActionValue("r-1", eventrule.MuteScopeRule, "HighCPU")
	assert.NoError(t, err)
	assert.JSONEq(t, `{"mute_event_rule_id":"r-1","mute_scope":"rule","mute_alert":"HighCPU"}`, v)
	// action_value is JSONB; CreateTriageRule's validator must accept it.
	assert.NoError(t, validateJSONBField(&v, "action_value"))

	// An event that maps to no alert rule carries no marker.
	v, err = muteActionValue("", eventrule.MuteScopeWorkload, "report_crash_loop")
	assert.NoError(t, err)
	assert.JSONEq(t, `{"mute_scope":"workload","mute_alert":"report_crash_loop"}`, v)
}

func TestIsMuteRule(t *testing.T) {
	str := func(s string) *string { return &s }
	assert.True(t, isMuteRule(&TriageRule{ActionValue: str(`{"mute_event_rule_id":"r-1"}`)}), "Alert Manager mute from before scopes")
	assert.True(t, isMuteRule(&TriageRule{ActionValue: str(`{"mute_scope":"workload","mute_alert":"a"}`)}))
	assert.False(t, isMuteRule(&TriageRule{ActionValue: str(`{"reason":"noise"}`)}))
	assert.False(t, isMuteRule(&TriageRule{ActionValue: str(`not json`)}))
	assert.False(t, isMuteRule(&TriageRule{}))

	// The winning suppression says whether it was a mute, so the firing's
	// classification is recorded as muted rather than as known noise.
	assert.True(t, applySuppressionRule(&TriageRule{Action: ActionSuppress, ActionValue: str(`{"mute_scope":"rule"}`)}).Muted)
	assert.False(t, applySuppressionRule(&TriageRule{Action: ActionSuppress}).Muted)
}

// AC-9: Alert Tuning's direct option is the resolver's answer for the operation
// the recommendation performs.
func TestDirectCapability(t *testing.T) {
	yes := eventrule.Capability{Available: true}
	noDisable := eventrule.Capability{Reason: "no disable"}
	noDuration := eventrule.Capability{Reason: "no duration"}
	ops := eventrule.RuleOperations{UpdateThreshold: yes, UpdateDuration: noDuration, DisableAtSource: noDisable}

	assert.Equal(t, yes, directCapability(ops, "tune_threshold"))
	assert.Equal(t, noDisable, directCapability(ops, "disable"))
	assert.Equal(t, noDuration, directCapability(ops, "increase_duration"))
	assert.Equal(t, noDuration, directCapability(ops, "tune_both"))

	ops.UpdateDuration = yes
	assert.Equal(t, yes, directCapability(ops, "tune_both"))
}
