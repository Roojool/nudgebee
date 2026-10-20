package api

import (
	"slices"
	"strings"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/promql/parser"
)

// remediation_alert_rule.go lets the remediation plan carry a change to the alert
// rule itself (docs/nubi-alert-rule-fix-spec.md). The model proposes it; nothing
// here trusts the model: the change must be one the rule allows (the panel sends the
// rule's live capabilities), an edit must be valid PromQL that differs from the rule,
// and anything else is dropped with its reason recorded.

// artifactAlertRule names the rule controls in available_artifacts. It admits only
// actions carrying alert_rule_change, never other command-less actions.
const artifactAlertRule = "alert_rule"

// Alert-rule operations.
const (
	alertRuleOpEdit    = "edit"
	alertRuleOpDisable = "disable"
	alertRuleOpMute    = "mute"
)

// RemediationAlertRule is the rule the event came from and what may be changed on it,
// as the api-server reported it when the plan was requested.
type RemediationAlertRule struct {
	RuleID            string   `json:"rule_id"`
	Alert             string   `json:"alert"`
	AllowedOperations []string `json:"allowed_operations"`
	MuteScopes        []string `json:"mute_scopes"`
	CurrentExpr       string   `json:"current_expr"`
	CurrentDuration   string   `json:"current_duration"`
}

// AlertRuleChange is a change to the alert rule proposed as a remediation action.
// Operation, Expr, Duration and MuteScope come from the model; the rest is filled in
// by the server so the panel can apply it and show when it is already applied.
type AlertRuleChange struct {
	Operation string `json:"operation"`
	Expr      string `json:"expr,omitempty"`
	Duration  string `json:"duration,omitempty"`
	MuteScope string `json:"mute_scope,omitempty"`

	RuleID           string `json:"rule_id,omitempty"`
	ExpectedExpr     string `json:"expected_expr,omitempty"`
	ExpectedDuration string `json:"expected_duration,omitempty"`
	ExprNormalized   string `json:"expr_normalized,omitempty"`
}

// DroppedAction records an action the server removed from the plan, so "no card"
// can be told apart from "the model did not propose one". Not rendered.
type DroppedAction struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

var remediationPromQLParser = parser.NewParser(parser.Options{})

func normalizePromQL(expr string) (string, bool) {
	if strings.TrimSpace(expr) == "" {
		return "", false
	}
	node, err := remediationPromQLParser.ParseExpr(expr)
	if err != nil {
		return "", false
	}
	return node.String(), true
}

// checkAlertRuleChange validates a proposed rule change against the rule and fills
// in what the panel needs to apply it. It returns a reason when the change must be
// dropped.
func checkAlertRuleChange(c *AlertRuleChange, rule *RemediationAlertRule) string {
	if rule == nil {
		return "no alert rule was given for this event"
	}
	op := strings.ToLower(strings.TrimSpace(c.Operation))
	c.Operation = op
	if !slices.Contains(rule.AllowedOperations, op) {
		return "operation " + op + " is not allowed on this rule"
	}
	// A mute is built from the event; editing or turning off needs the rule row.
	if op != alertRuleOpMute && rule.RuleID == "" {
		return "no alert rule row for this event"
	}
	c.RuleID = rule.RuleID
	switch op {
	case alertRuleOpEdit:
		current, ok := normalizePromQL(rule.CurrentExpr)
		if !ok {
			return "the rule's current expression is not PromQL"
		}
		expr := strings.TrimSpace(c.Expr)
		norm := current
		if expr != "" {
			var valid bool
			if norm, valid = normalizePromQL(expr); !valid {
				return "the proposed expression is not valid PromQL"
			}
		}
		duration := strings.TrimSpace(c.Duration)
		if duration != "" {
			if _, err := model.ParseDuration(duration); err != nil {
				return "the proposed duration is not valid"
			}
		}
		exprChanged := norm != current
		durationChanged := duration != "" && duration != strings.TrimSpace(rule.CurrentDuration)
		if !exprChanged && !durationChanged {
			return "the proposed change does not modify the rule"
		}
		if !exprChanged {
			expr = ""
		}
		if !durationChanged {
			duration = ""
		}
		c.Expr, c.Duration = expr, duration
		c.ExpectedExpr, c.ExpectedDuration = rule.CurrentExpr, rule.CurrentDuration
		c.ExprNormalized = norm
		c.MuteScope = ""
	case alertRuleOpDisable:
		c.Expr, c.Duration, c.MuteScope = "", "", ""
	case alertRuleOpMute:
		c.MuteScope = strings.ToLower(strings.TrimSpace(c.MuteScope))
		if !slices.Contains(rule.MuteScopes, c.MuteScope) {
			return "mute scope " + c.MuteScope + " is not offered for this event"
		}
		c.Expr, c.Duration = "", ""
	}
	return ""
}

// alertRuleActionKind is the kind a rule change always has: fixing or turning off
// the rule removes the cause of a false positive; a mute only hides it.
func alertRuleActionKind(op string) string {
	if op == alertRuleOpMute {
		return RemediationKindMitigation
	}
	return RemediationKindFix
}
