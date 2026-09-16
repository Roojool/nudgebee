package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"nudgebee/services/eventrule"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// mute.go implements "Mute in Nudgebee" for an alert rule (epic #38553, spec D1):
// a set of ordinary suppression rules, one per event source the alert arrives on,
// tied to the event_rules row by action_value.mute_event_rule_id. Events keep being
// stored and resolve normally; they are marked SUPPRESSED, which already skips
// notifications and LLM analysis. The customer's rule is not touched.

// MuteAlertnamePattern is the match_alertname for a muted rule. match_alertname is
// an unanchored regex, so the name is escaped and anchored: "API (5xx) > 1%" must
// match itself and not "API (5xx) > 1% [staging]".
func MuteAlertnamePattern(alert string) string {
	return "^" + regexp.QuoteMeta(alert) + "$"
}

func muteActionValue(ruleID string) (string, error) {
	b, err := json.Marshal(map[string]string{eventrule.MuteMarkerKey: ruleID})
	if err != nil {
		return "", fmt.Errorf("mute: marshal action_value: %w", err)
	}
	return string(b), nil
}

// UpdateRuleMuteRequest is the eventrules_update_mute input.
type UpdateRuleMuteRequest struct {
	RuleID    string `json:"rule_id"`
	AccountID string `json:"account_id"`
	Muted     bool   `json:"muted"`
}

// UpdateRuleMute mutes or unmutes an alert rule and returns its fresh capabilities.
func UpdateRuleMute(ctx *security.RequestContext, req UpdateRuleMuteRequest) (eventrule.RuleCapabilities, error) {
	sc := ctx.GetSecurityContext()
	if sc == nil || sc.GetTenantId() == "" || sc.GetUserId() == "" {
		return eventrule.RuleCapabilities{}, fmt.Errorf("unauthorized")
	}
	if req.RuleID == "" || req.AccountID == "" {
		return eventrule.RuleCapabilities{}, fmt.Errorf("rule_id and account_id are required")
	}
	if !sc.HasAccountAccess(req.AccountID, security.SecurityAccessTypeUpdate) {
		return eventrule.RuleCapabilities{}, fmt.Errorf("account unauthorized")
	}

	target, err := eventrule.ResolveRuleTarget(ctx, req.RuleID, eventrule.ResolveOptions{})
	if err != nil {
		return eventrule.RuleCapabilities{}, err
	}
	if target.AccountID != req.AccountID {
		return eventrule.RuleCapabilities{}, eventrule.ErrRuleNotFound
	}
	if req.Muted && !target.Operations.Mute.Available {
		return eventrule.RuleCapabilities{}, fmt.Errorf("%w: mute: %s", eventrule.ErrOperationUnavailable, target.Operations.Mute.Reason)
	}

	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return eventrule.RuleCapabilities{}, fmt.Errorf("mute: database manager: %w", err)
	}

	if req.Muted {
		err = muteRule(ctx.GetContext(), dbms.Db, sc.GetTenantId(), req.AccountID, sc.GetUserId(),
			target.ID, target.Rule.Alert, eventrule.MuteEventSources(target.Rule.Source))
	} else {
		err = unmuteRule(ctx.GetContext(), dbms.Db, sc.GetTenantId(), req.AccountID, sc.GetUserId(), target.ID)
	}
	if err != nil {
		return eventrule.RuleCapabilities{}, err
	}
	// Other replicas pick the change up when their rule cache expires (triageRulesTTL).
	ClearTriageRulesCache()

	caps := target.Capabilities()
	caps.Muted = req.Muted
	return caps, nil
}

// muteRule enables one suppression rule per source, reusing rows from an earlier
// mute so repeated mute/unmute does not pile up rules.
func muteRule(ctx context.Context, db *sqlx.DB, tenantID, accountID, userID, ruleID, alert string, sources []string) error {
	if len(sources) == 0 {
		return fmt.Errorf("%w: mute: no event sources for this rule", eventrule.ErrOperationUnavailable)
	}
	actionValue, err := muteActionValue(ruleID)
	if err != nil {
		return err
	}
	pattern := MuteAlertnamePattern(alert)
	name := "Muted alert rule: " + alert
	description := "Created by Mute in Alert Manager. Events from this alert rule are kept but marked suppressed."

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mute: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, source := range sources {
		var existingID string
		err := tx.GetContext(ctx, &existingID, `
			SELECT id FROM event_triage_rules
			WHERE tenant_id = $1 AND account_id = $2 AND match_source = $3
			  AND action_value->>'`+eventrule.MuteMarkerKey+`' = $4
			ORDER BY created_at LIMIT 1`, tenantID, accountID, source, ruleID)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `
				UPDATE event_triage_rules
				SET enabled = TRUE, match_alertname = $2, rule_type = $3, action = $4,
				    effective_until = NULL, updated_by = $5, updated_at = NOW()
				WHERE id = $1`, existingID, pattern, RuleTypeSuppression, ActionSuppress, userID); err != nil {
				return fmt.Errorf("mute: re-enable rule for %s: %w", source, err)
			}
		case errors.Is(err, sql.ErrNoRows):
			src := source
			now := time.Now().UTC()
			rule := &TriageRule{
				ID:             uuid.New().String(),
				TenantID:       &tenantID,
				AccountID:      &accountID,
				RuleType:       RuleTypeSuppression,
				MatchSource:    &src,
				MatchAlertname: &pattern,
				Action:         ActionSuppress,
				ActionValue:    &actionValue,
				Priority:       200,
				IsEditable:     true,
				CanOverride:    true,
				Enabled:        true,
				Name:           &name,
				Description:    &description,
				CreatedBy:      &userID,
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			if err := insertTriageRuleTx(ctx, tx, rule); err != nil {
				return fmt.Errorf("mute: insert rule for %s: %w", source, err)
			}
		default:
			return fmt.Errorf("mute: look up rule for %s: %w", source, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mute: commit: %w", err)
	}
	return nil
}

// unmuteRule disables every suppression rule tied to the alert rule.
func unmuteRule(ctx context.Context, db *sqlx.DB, tenantID, accountID, userID, ruleID string) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE event_triage_rules
		SET enabled = FALSE, updated_by = $4, updated_at = NOW()
		WHERE tenant_id = $1 AND account_id = $2
		  AND action_value->>'`+eventrule.MuteMarkerKey+`' = $3 AND enabled`,
		tenantID, accountID, ruleID, userID); err != nil {
		return fmt.Errorf("unmute: %w", err)
	}
	return nil
}
