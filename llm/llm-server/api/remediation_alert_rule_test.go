package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otelRuleExpr = `sum by (service_name) (rate(rpc_client_duration_count{namespace="demo", rpc_grpc_status_code!="0"}[2m])) / sum by (service_name) (rate(rpc_client_duration_count{namespace="demo"}[2m])) > 0.05`

const otelFixedExpr = `sum by (service_name) (rate(rpc_client_duration_count{namespace="demo", rpc_grpc_status_code!="0", rpc_method!="EventStream"}[2m])) / sum by (service_name) (rate(rpc_client_duration_count{namespace="demo", rpc_method!="EventStream"}[2m])) > 0.05`

func otelRule() *RemediationAlertRule {
	return &RemediationAlertRule{
		RuleID:            "r-1",
		Alert:             "OtelDemoGRPCClientErrorRate",
		AllowedOperations: []string{alertRuleOpEdit, alertRuleOpDisable, alertRuleOpMute},
		MuteScopes:        []string{"workload", "rule"},
		CurrentExpr:       otelRuleExpr,
		CurrentDuration:   "1m",
	}
}

func ruleAction(name string, change AlertRuleChange) RemediationAction {
	return RemediationAction{Action: name, Kind: "fix", Confidence: 90, AlertRuleChange: &change,
		ExecuteCommand: "kubectl edit prometheusrule x", VerifyCommand: "kubectl get x", RollbackCommand: "kubectl apply -f y"}
}

// A valid edit is kept as a fix, with no commands, carrying what the panel needs to
// apply it and to tell when it is already applied.
func TestAlertRuleEditKept(t *testing.T) {
	plan := RemediationPlan{Actions: []RemediationAction{ruleAction("Fix OtelDemoGRPCClientErrorRate rule", AlertRuleChange{Operation: "Edit", Expr: otelFixedExpr})}}
	normalizePlan(&plan, []string{artifactAlertRule}, otelRule())

	require.Len(t, plan.Actions, 1)
	a := plan.Actions[0]
	assert.Equal(t, RemediationKindFix, a.Kind)
	assert.Empty(t, a.ExecuteCommand)
	assert.Empty(t, a.VerifyCommand)
	assert.Empty(t, a.RollbackCommand)
	c := a.AlertRuleChange
	assert.Equal(t, alertRuleOpEdit, c.Operation)
	assert.Equal(t, "r-1", c.RuleID)
	assert.Equal(t, otelRuleExpr, c.ExpectedExpr)
	assert.Equal(t, "1m", c.ExpectedDuration)
	assert.NotEmpty(t, c.ExprNormalized)
	assert.Empty(t, c.Duration, "for unchanged is not sent")
	assert.Empty(t, plan.Dropped)
}

func TestAlertRuleChangeDropped(t *testing.T) {
	cases := []struct {
		name   string
		change AlertRuleChange
		rule   *RemediationAlertRule
		reason string
	}{
		{"invalid PromQL", AlertRuleChange{Operation: "edit", Expr: "sum(rate(x[5m]"}, otelRule(), "not valid PromQL"},
		{"no change", AlertRuleChange{Operation: "edit", Expr: otelRuleExpr + "  "}, otelRule(), "does not modify"},
		{"invalid duration", AlertRuleChange{Operation: "edit", Duration: "ten minutes"}, otelRule(), "duration is not valid"},
		{"operation not allowed", AlertRuleChange{Operation: "disable"}, &RemediationAlertRule{RuleID: "r", AllowedOperations: []string{alertRuleOpMute}, MuteScopes: []string{"rule"}}, "not allowed"},
		{"unknown mute scope", AlertRuleChange{Operation: "mute", MuteScope: "namespace"}, otelRule(), "not offered"},
		{"no rule for the event", AlertRuleChange{Operation: "mute", MuteScope: "rule"}, nil, "no alert rule"},
		{"current expression is not PromQL", AlertRuleChange{Operation: "edit", Expr: "up == 0"}, &RemediationAlertRule{RuleID: "r", AllowedOperations: []string{alertRuleOpEdit}, CurrentExpr: "rate(x) keep_metric_names"}, "not PromQL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := RemediationPlan{Actions: []RemediationAction{ruleAction(tc.name, tc.change)}}
			normalizePlan(&plan, []string{artifactAlertRule}, tc.rule)
			assert.Empty(t, plan.Actions)
			require.Len(t, plan.Dropped, 1)
			assert.Equal(t, tc.name, plan.Dropped[0].Action)
			assert.Contains(t, plan.Dropped[0].Reason, tc.reason)
		})
	}
}

// Turning the rule off removes the cause of a false positive; a mute only hides it.
func TestAlertRuleKinds(t *testing.T) {
	plan := RemediationPlan{Actions: []RemediationAction{
		ruleAction("Mute fraud-detection", AlertRuleChange{Operation: "mute", MuteScope: "Workload"}),
		{Action: "Turn off rule", Kind: "mitigation", Confidence: 70, AlertRuleChange: &AlertRuleChange{Operation: "disable", Expr: "ignored"}},
	}}
	normalizePlan(&plan, []string{artifactAlertRule}, otelRule())
	require.Len(t, plan.Actions, 2)
	// Ranked: the fix first.
	assert.Equal(t, "Turn off rule", plan.Actions[0].Action)
	assert.Equal(t, RemediationKindFix, plan.Actions[0].Kind)
	assert.Equal(t, 70, int(plan.Actions[0].Confidence), "a fix mislabelled as mitigation keeps its confidence")
	assert.Empty(t, plan.Actions[0].AlertRuleChange.Expr)
	assert.Equal(t, RemediationKindMitigation, plan.Actions[1].Kind)
	assert.Equal(t, "workload", plan.Actions[1].AlertRuleChange.MuteScope)
	assert.LessOrEqual(t, int(plan.Actions[1].Confidence), maxMitigationConfidence)
}

// The alert-rule controls are not a prepared fix for anything else: with only them
// available, a command-less action that does not change the rule is still dropped.
func TestAlertRuleArtifactAdmitsOnlyRuleChanges(t *testing.T) {
	plan := RemediationPlan{Actions: []RemediationAction{
		{Action: "Rebuild the image", Kind: "fix", Confidence: 80},
		ruleAction("Fix rule", AlertRuleChange{Operation: "edit", Duration: "10m"}),
	}}
	normalizePlan(&plan, []string{artifactAlertRule}, otelRule())
	require.Len(t, plan.Actions, 1)
	assert.Equal(t, "Fix rule", plan.Actions[0].Action)
	assert.Equal(t, "10m", plan.Actions[0].AlertRuleChange.Duration)

	// With a real artifact (a code fix) the command-less action is admitted as before.
	plan = RemediationPlan{Actions: []RemediationAction{{Action: "Apply code fix", Kind: "fix", Confidence: 80}}}
	normalizePlan(&plan, []string{artifactAlertRule, "code_fix"}, otelRule())
	assert.Len(t, plan.Actions, 1)
}
