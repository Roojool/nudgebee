package eventrule

import (
	stdctx "context"
	"fmt"
	"slices"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// writableRuleSources are rule sources whose row describes the rule where it is
// defined (Nudgebee can change it there). Other rows with the same alert name are
// copies a webhook integration wrote when it first saw the alert.
var writableRuleSources = map[string]bool{
	agentEventSourceName:     true,
	legacyUISourceName:       true,
	prometheusUserSourceName: true,
	"AWS_CloudWatch_Alarm":   true,
	"GCP_Metric_Alert":       true,
	"Azure_Monitor_Alert":    true,
}

// RuleSourcesForEvent returns the rule sources an event from eventSource can
// belong to: its own source, plus every rule source whose alerts are delivered on
// it (a pagerduty_webhook event can be a prometheus rule firing).
func RuleSourcesForEvent(eventSource string) []string {
	out := []string{eventSource}
	for ruleSource, eventSources := range muteEventSources {
		if ruleSource != eventSource && slices.Contains(eventSources, eventSource) {
			out = append(out, ruleSource)
		}
	}
	slices.Sort(out[1:])
	return out
}

type eventRuleCandidate struct {
	ID     string `db:"id"`
	Source string `db:"source"`
}

// pickEventRule chooses the rule an event belongs to. The row that defines the
// rule wins over webhook copies of it (a Prometheus alert routed through
// Alertmanager has both a prometheus row and a prometheus_alertmanager_webhook
// copy). Two definitions, or two copies and no definition, are ambiguous.
func pickEventRule(cands []eventRuleCandidate) (string, bool) {
	var defs, copies []string
	for _, c := range cands {
		if writableRuleSources[c.Source] {
			defs = append(defs, c.ID)
		} else {
			copies = append(copies, c.ID)
		}
	}
	switch {
	case len(defs) == 1:
		return defs[0], true
	case len(defs) == 0 && len(copies) == 1:
		return copies[0], true
	default:
		return "", false
	}
}

// FindEventRuleID returns the event_rules row an event belongs to, or "" when
// there is none or the choice is ambiguous. alert is the event's aggregation_key.
func FindEventRuleID(ctx stdctx.Context, db *sqlx.DB, tenantID, accountID, eventSource, alert string) (string, error) {
	if alert == "" || eventSource == "" {
		return "", nil
	}
	var cands []eventRuleCandidate
	if err := db.SelectContext(ctx, &cands, `
		SELECT id, source FROM event_rules
		WHERE tenant_id = $1 AND account_id = $2 AND alert = $3 AND source = ANY($4)`,
		tenantID, accountID, alert, pq.Array(RuleSourcesForEvent(eventSource))); err != nil {
		return "", fmt.Errorf("eventrule: find rule for event: %w", err)
	}
	id, _ := pickEventRule(cands)
	return id, nil
}
