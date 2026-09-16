package eventrule

import (
	stdctx "context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"nudgebee/services/internal/database"
	"nudgebee/services/observability/alertrule"
	"nudgebee/services/security"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// target.go is the single answer to "where does this event_rules row live, and
// which changes can Nudgebee really make to it?" (epic #38553, spec D2). Alert
// Manager, Alert Tuning and the capability RPC all read it, so a control is only
// offered when the write behind it reaches the system that evaluates the rule.

// RuleTargetKind names the system that evaluates a rule.
type RuleTargetKind string

const (
	TargetAWS         RuleTargetKind = "aws"
	TargetGCP         RuleTargetKind = "gcp"
	TargetAzure       RuleTargetKind = "azure"
	TargetPromCR      RuleTargetKind = "prom_cr"     // PrometheusRule CR through the k8s agent
	TargetPromRuler   RuleTargetKind = "prom_ruler"  // account's own ruler (prometheus_user)
	TargetExternal    RuleTargetKind = "external"    // rule Nudgebee created in a SaaS provider
	TargetUnsupported RuleTargetKind = "unsupported" // Nudgebee holds only a copy
)

// Capability says whether one operation is available and, if not, why.
// Warning is set on an available operation the user should double-check.
type Capability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

// RuleOperations lists every rule change a UI (or Nubi) may offer.
type RuleOperations struct {
	// EditDefinition is the Alert Manager edit form (expr, for, annotations).
	EditDefinition  Capability `json:"edit_definition"`
	UpdateThreshold Capability `json:"update_threshold"`
	UpdateDuration  Capability `json:"update_duration"`
	DisableAtSource Capability `json:"disable_at_source"`
	Mute            Capability `json:"mute"`
	PR              Capability `json:"pr"`
}

// RuleCapabilities is the per-rule answer of eventrules_get_capabilities.
type RuleCapabilities struct {
	RuleID     string         `json:"rule_id"`
	Alert      string         `json:"alert"`
	Source     string         `json:"source"`
	Target     RuleTargetKind `json:"target"`
	Muted      bool           `json:"muted"`
	Operations RuleOperations `json:"operations"`
}

// RuleLocator is what a provider write needs to find the upstream rule.
type RuleLocator struct {
	AWSRegion    string `json:"aws_region,omitempty"`
	AWSAlarmName string `json:"aws_alarm_name,omitempty"`
	AWSAlarmARN  string `json:"aws_alarm_arn,omitempty"`
	GCPPolicy    string `json:"gcp_policy,omitempty"`
	AzureID      string `json:"azure_id,omitempty"`
	// AzureName is the alert name cloud-collector searches every subscription
	// for when no ARM id is known (until sync stores one).
	AzureName  string `json:"azure_name,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
}

// RuleTarget is a resolved rule: the row, where it lives, and what can be done.
type RuleTarget struct {
	ID         string
	AccountID  string
	Rule       toggledRule
	Category   string
	Enabled    bool
	IsEditable bool
	Ambiguous  bool
	Origin     string
	Kind       RuleTargetKind
	Locator    RuleLocator
	Muted      bool
	Operations RuleOperations
}

// Capabilities is the public view of a resolved target.
func (t *RuleTarget) Capabilities() RuleCapabilities {
	return RuleCapabilities{
		RuleID:     t.ID,
		Alert:      t.Rule.Alert,
		Source:     t.Rule.Source,
		Target:     t.Kind,
		Muted:      t.Muted,
		Operations: t.Operations,
	}
}

// ErrRuleNotFound is returned for an id that does not exist in the caller's
// tenant or belongs to an account the caller cannot read. The two cases are
// deliberately indistinguishable.
var ErrRuleNotFound = errors.New("alert rule not found")

// ErrOperationUnavailable wraps the reason an operation cannot be applied.
var ErrOperationUnavailable = errors.New("operation not available for this alert rule")

// ResolveOptions carries facts the caller knows better than the row does.
type ResolveOptions struct {
	// AlertRuleKey is the upstream id an event carried (AWS alarm ARN, GCP
	// policy name, Azure alert id). Alert Tuning has it; Alert Manager does not.
	AlertRuleKey string
}

// muteEventSources maps a rule source to the event sources whose events carry
// that rule's alert name as aggregation_key. Mute is offered only for these:
// for webhook sources the key is often a monitor or incident id (verified on
// dev: datadog_webhook 44%, zenduty_webhook 60%), so a name-based mute would
// silently miss events.
var muteEventSources = map[string][]string{
	"AWS_CloudWatch_Alarm": {"AWS_CloudWatch_Alarm", "AWS_EventBridge"},
	"AWS_EventBridge":      {"AWS_EventBridge"},
	"GCP_Metric_Alert":     {"GCP_Metric_Alert"},
	"Azure_Monitor_Alert":  {"Azure_Monitor_Alert"},
	agentEventSourceName:   {agentEventSourceName},
	legacyUISourceName:     {agentEventSourceName},
}

// MuteEventSources returns the event sources a mute of this rule source covers,
// or nil when mute is not offered for it.
func MuteEventSources(ruleSource string) []string {
	return muteEventSources[ruleSource]
}

// legacyUISourceName is the source older UI-created PrometheusRule entries were
// stored under before normalizeEventSource mapped it to "prometheus".
const legacyUISourceName = "nudgebee"

const azureScheduledQueryCategory = "Azure Scheduled Query Alert"

// isAgentPrometheusRow reports whether a row's rule is written to a
// PrometheusRule CR through the agent.
func isAgentPrometheusRow(source, alertType string) bool {
	if source == agentEventSourceName {
		return true
	}
	return source == legacyUISourceName && (alertType == "" || alertType == "metric")
}

// targetKindForSource is the one source → target map (replaces the scattered
// isExternalProviderSource / cloudAlertProvider checks for routing decisions).
func targetKindForSource(source, alertType string) RuleTargetKind {
	switch {
	case isAgentPrometheusRow(source, alertType):
		return TargetPromCR
	case source == prometheusUserSourceName:
		return TargetPromRuler
	case source == "AWS_CloudWatch_Alarm":
		return TargetAWS
	case source == "GCP_Metric_Alert":
		return TargetGCP
	case source == "Azure_Monitor_Alert", source == "azure_monitor_webhook":
		// The webhook twin describes the same Azure alert; triage apply has always
		// routed it to Azure.
		return TargetAzure
	case isExternalProviderSource(source):
		return TargetExternal
	}
	return TargetUnsupported
}

// CloudProviderKey is the alertrule provider key for a cloud target.
func CloudProviderKey(kind RuleTargetKind) string {
	switch kind {
	case TargetAWS:
		return "aws_cloudwatch"
	case TargetGCP:
		return "gcp_monitoring"
	case TargetAzure:
		return "azure_app_insights"
	}
	return ""
}

type ruleRow struct {
	ID             string         `db:"id"`
	AccountID      string         `db:"account_id"`
	Alert          string         `db:"alert"`
	Source         sql.NullString `db:"source"`
	Expr           sql.NullString `db:"expr"`
	Duration       sql.NullString `db:"duration"`
	Severity       sql.NullString `db:"severity"`
	Category       sql.NullString `db:"category"`
	AlertType      sql.NullString `db:"alert_type"`
	ExternalRuleID sql.NullString `db:"external_rule_id"`
	Annotations    sql.NullString `db:"annotations"`
	Labels         sql.NullString `db:"labels"`
	ProviderConfig sql.NullString `db:"provider_config"`
	Enabled        sql.NullBool   `db:"enabled"`
	IsEditable     sql.NullBool   `db:"is_editable"`
	Ambiguous      sql.NullBool   `db:"ambiguous"`
	Origin         sql.NullString `db:"origin"`
}

const ruleRowColumns = `id, account_id, alert, source, expr, duration, severity, category, alert_type,
	external_rule_id, annotations::text AS annotations, labels::text AS labels,
	provider_config::text AS provider_config, enabled, is_editable`

// loadRuleRows reads rows by id within one tenant. The ambiguous/origin columns
// are read when present; if the migration has not run yet the query is retried
// without them (fail open: unknown ambiguity is treated as not ambiguous).
func loadRuleRows(ctx stdctx.Context, db *sqlx.DB, tenantID string, ids []string) ([]ruleRow, error) {
	var rows []ruleRow
	query := `SELECT ` + ruleRowColumns + `, ambiguous, origin FROM event_rules WHERE tenant_id = $1 AND id = ANY($2::uuid[])`
	err := db.SelectContext(ctx, &rows, query, tenantID, pq.Array(ids))
	var pqErr *pq.Error
	if err != nil && errors.As(err, &pqErr) && pqErr.Code == "42703" {
		rows = nil
		err = db.SelectContext(ctx, &rows,
			`SELECT `+ruleRowColumns+` FROM event_rules WHERE tenant_id = $1 AND id = ANY($2::uuid[])`,
			tenantID, pq.Array(ids))
	}
	if err != nil {
		return nil, fmt.Errorf("eventrule: load rules: %w", err)
	}
	return rows, nil
}

func (r ruleRow) toTarget() *RuleTarget {
	t := &RuleTarget{
		ID:         r.ID,
		AccountID:  r.AccountID,
		Category:   r.Category.String,
		Enabled:    !r.Enabled.Valid || r.Enabled.Bool,
		IsEditable: r.IsEditable.Bool,
		Ambiguous:  r.Ambiguous.Bool,
		Origin:     r.Origin.String,
		Rule: toggledRule{
			Alert:       r.Alert,
			Source:      r.Source.String,
			Expr:        r.Expr.String,
			Duration:    r.Duration.String,
			Severity:    r.Severity.String,
			AlertType:   r.AlertType.String,
			Annotations: unmarshalStringMap(nullStringPtr(r.Annotations)),
			Labels:      unmarshalStringMap(nullStringPtr(r.Labels)),
		},
	}
	if r.ExternalRuleID.Valid && r.ExternalRuleID.String != "" {
		id := r.ExternalRuleID.String
		t.Rule.ExternalRuleID = &id
	}
	if r.ProviderConfig.Valid && r.ProviderConfig.String != "" {
		_ = json.Unmarshal([]byte(r.ProviderConfig.String), &t.Rule.ProviderConfig)
	}
	t.Kind = targetKindForSource(t.Rule.Source, t.Rule.AlertType)
	return t
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// buildLocator derives the upstream locator from the row (and the caller's hint).
func buildLocator(t *RuleTarget, opts ResolveOptions) RuleLocator {
	loc := RuleLocator{ExternalID: derefString(t.Rule.ExternalRuleID)}
	switch t.Kind {
	case TargetAWS:
		loc.AWSRegion = t.Rule.Labels["aws_region"]
		loc.AWSAlarmName = t.Rule.Labels["aws_alarm"]
		var def struct {
			AlarmArn  string `json:"AlarmArn"`
			AlarmName string `json:"AlarmName"`
		}
		if json.Unmarshal([]byte(t.Rule.Expr), &def) == nil {
			loc.AWSAlarmARN = def.AlarmArn
			if loc.AWSAlarmName == "" {
				loc.AWSAlarmName = def.AlarmName
			}
		}
		if strings.HasPrefix(opts.AlertRuleKey, "arn:") {
			loc.AWSAlarmARN = opts.AlertRuleKey
		}
		if loc.AWSAlarmARN == "" && strings.HasPrefix(loc.ExternalID, "arn:") {
			loc.AWSAlarmARN = loc.ExternalID
		}
	case TargetGCP:
		loc.GCPPolicy = firstNonEmpty(loc.ExternalID, t.Rule.Labels["gcp_policy_id"], opts.AlertRuleKey)
	case TargetAzure:
		loc.AzureID = firstNonEmpty(loc.ExternalID, azureIDFromKey(opts.AlertRuleKey))
		loc.AzureName = firstNonEmpty(opts.AlertRuleKey, t.Rule.Alert)
	}
	return loc
}

// azureIDFromKey accepts only an ARM resource id; a name goes to AzureName.
func azureIDFromKey(key string) string {
	if strings.HasPrefix(strings.ToLower(key), "/subscriptions/") {
		return key
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// capabilityEnv is everything outside the row that decides availability,
// injected so the decision table is unit-testable.
type capabilityEnv struct {
	agentConnected  func(accountID string) bool
	rulerConfigured func(accountID string) (bool, string)
	hasGit          bool
}

func unavailable(reason string) Capability { return Capability{Reason: reason} }

var available = Capability{Available: true}

// computeOperations is the decision table (spec D1/D2). Pure: no I/O besides env.
func computeOperations(t *RuleTarget, env capabilityEnv) RuleOperations {
	var ops RuleOperations

	if len(muteEventSources[t.Rule.Source]) > 0 {
		ops.Mute = available
	} else {
		ops.Mute = unavailable("events from this source are not keyed by the rule name, so they cannot be muted by rule yet — add a suppression rule in Triage instead")
	}

	const ambiguousReason = "more than one upstream rule shares this name, so Nudgebee cannot tell which one to change"
	notYet := func(what string) Capability {
		return unavailable(what + " is not supported for this source yet")
	}

	switch t.Kind {
	case TargetPromCR:
		pr := unavailable("no GitHub/GitLab integration configured")
		if env.hasGit {
			pr = available
		}
		ops.PR = pr
		var write Capability
		switch {
		case !t.IsEditable:
			write = unavailable("this rule was read from the Prometheus rules API; Nudgebee does not know which PrometheusRule defines it")
		case t.Ambiguous:
			write = unavailable(ambiguousReason)
		case !env.agentConnected(t.AccountID):
			write = unavailable("k8s agent not connected — cannot change the PrometheusRule")
		default:
			write = available
		}
		ops.EditDefinition, ops.UpdateThreshold, ops.UpdateDuration, ops.DisableAtSource = write, write, write, write

	case TargetPromRuler:
		ops.PR = unavailable("PR apply is not supported for rules in a hosted ruler")
		write := available
		if ok, reason := env.rulerConfigured(t.AccountID); !ok {
			write = unavailable(reason)
		}
		ops.EditDefinition, ops.UpdateThreshold, ops.UpdateDuration, ops.DisableAtSource = write, write, write, write

	case TargetExternal:
		ops.PR = unavailable("PR apply is not supported for this source")
		write := available
		if t.Rule.ExternalRuleID == nil || *t.Rule.ExternalRuleID == "" {
			write = unavailable("this rule has no id in the provider, so Nudgebee cannot change it there")
		}
		ops.EditDefinition, ops.UpdateThreshold, ops.UpdateDuration, ops.DisableAtSource = write, write, write, write

	case TargetAWS:
		ops.PR = unavailable("PR apply is not supported for CloudWatch alarms")
		ops.EditDefinition = unavailable("CloudWatch alarm thresholds are changed from Alert Tuning")
		ops.DisableAtSource = unavailable("CloudWatch has no per-alarm disable, and disabling alarm actions would silence your own paging — use Mute instead")
		write := available
		switch {
		case t.Ambiguous && t.Locator.AWSAlarmARN == "":
			write = unavailable(ambiguousReason)
		case t.Locator.AWSAlarmARN == "" && (t.Locator.AWSRegion == "" || t.Locator.AWSAlarmName == ""):
			write = unavailable("the alarm's region and name are not known")
		}
		ops.UpdateThreshold, ops.UpdateDuration = write, write

	case TargetGCP:
		ops.PR = unavailable("PR apply is not supported for GCP alert policies")
		ops.EditDefinition = unavailable("GCP alert policy thresholds are changed from Alert Tuning")
		ops.DisableAtSource = notYet("Disabling a GCP alert policy")
		write := Capability{Available: true, Warning: "direct apply for GCP is best-effort; verify the policy after applying"}
		switch {
		case t.Ambiguous && t.Locator.GCPPolicy == "":
			write = unavailable(ambiguousReason)
		case t.Locator.GCPPolicy == "":
			write = unavailable("the alert policy id is not known")
		}
		ops.UpdateThreshold, ops.UpdateDuration = write, write

	case TargetAzure:
		ops.PR = unavailable("PR apply is not supported for Azure alerts")
		ops.EditDefinition = unavailable("Azure alert thresholds are changed from Alert Tuning")
		ops.DisableAtSource = notYet("Disabling an Azure alert")
		write := Capability{Available: true, Warning: "direct apply for Azure is best-effort; verify the alert after applying"}
		switch {
		case t.Category == azureScheduledQueryCategory:
			write = unavailable("log search (scheduled query) alerts cannot be changed from Nudgebee")
		case t.Locator.AzureID != "":
		case t.Ambiguous:
			write = unavailable(ambiguousReason)
		default:
			write.Warning = "Nudgebee finds this Azure alert by name; if another alert in your subscriptions has the same name, verify which one changed"
		}
		ops.UpdateThreshold, ops.UpdateDuration = write, write

	default:
		reason := "Nudgebee only holds a copy of this rule; change it in the system that evaluates it"
		ops.EditDefinition = unavailable(reason)
		ops.UpdateThreshold = unavailable(reason)
		ops.UpdateDuration = unavailable(reason)
		ops.DisableAtSource = unavailable(reason)
		ops.PR = unavailable(reason)
	}
	return ops
}

// loadMutedRuleIDs returns which of the given event_rules ids have an enabled mute.
func loadMutedRuleIDs(ctx stdctx.Context, db *sqlx.DB, tenantID string, ids []string) (map[string]bool, error) {
	var muted []string
	err := db.SelectContext(ctx, &muted, `
		SELECT DISTINCT action_value->>'`+MuteMarkerKey+`'
		FROM event_triage_rules
		WHERE tenant_id = $1 AND enabled = TRUE
		  AND action_value->>'`+MuteMarkerKey+`' = ANY($2)`, tenantID, pq.Array(ids))
	if err != nil {
		return nil, fmt.Errorf("eventrule: load mutes: %w", err)
	}
	out := make(map[string]bool, len(muted))
	for _, id := range muted {
		out[id] = true
	}
	return out, nil
}

// MuteMarkerKey is the action_value key that ties a triage suppression rule to
// the event_rules row it mutes.
const MuteMarkerKey = "mute_event_rule_id"

// HasGitIntegration reports whether the tenant has an enabled github/gitlab integration.
func HasGitIntegration(ctx stdctx.Context, db *sqlx.DB, tenantID string) bool {
	var count int
	err := db.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM integrations
		WHERE tenant_id = $1 AND status = 'enabled' AND type IN ('github', 'gitlab')`, tenantID)
	return err == nil && count > 0
}

// ResolveRuleTargets resolves several rules for one tenant. Ids the caller may not
// read are omitted. The caller's tenant always scopes the lookup.
func ResolveRuleTargets(ctx *security.RequestContext, ids []string, opts ResolveOptions) ([]*RuleTarget, error) {
	sc := ctx.GetSecurityContext()
	if sc == nil || sc.GetTenantId() == "" {
		return nil, fmt.Errorf("unauthorized")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, fmt.Errorf("eventrule: database manager: %w", err)
	}
	return resolveRuleTargets(ctx, dbms.Db, ids, opts)
}

func resolveRuleTargets(ctx *security.RequestContext, db *sqlx.DB, ids []string, opts ResolveOptions) ([]*RuleTarget, error) {
	sc := ctx.GetSecurityContext()
	tenantID := sc.GetTenantId()

	// A malformed id would fail the uuid[] cast for the whole batch; it can match
	// no rule, so drop it and resolve the rest.
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := uuid.Parse(id); err == nil {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return nil, nil
	}
	ids = valid

	rows, err := loadRuleRows(ctx.GetContext(), db, tenantID, ids)
	if err != nil {
		return nil, err
	}
	muted, err := loadMutedRuleIDs(ctx.GetContext(), db, tenantID, ids)
	if err != nil {
		// A failed mute lookup must not hide the rule's other controls.
		ctx.GetLogger().Warn("eventrule: mute status lookup failed", "error", err)
		muted = map[string]bool{}
	}

	agentCache := map[string]bool{}
	rulerCache := map[string]Capability{}
	var hasGit *bool
	env := capabilityEnv{
		agentConnected: func(accountID string) bool {
			if v, ok := agentCache[accountID]; ok {
				return v
			}
			v := isK8sAgentConnected(accountID)
			agentCache[accountID] = v
			return v
		},
		rulerConfigured: func(accountID string) (bool, string) {
			if v, ok := rulerCache[accountID]; ok {
				return v.Available, v.Reason
			}
			ok, reason := alertrule.PrometheusRulerConfigured(ctx, accountID)
			rulerCache[accountID] = Capability{Available: ok, Reason: reason}
			return ok, reason
		},
	}

	out := make([]*RuleTarget, 0, len(rows))
	for _, r := range rows {
		if !sc.HasAccountAccess(r.AccountID, security.SecurityAccessTypeRead) {
			continue
		}
		t := r.toTarget()
		if t.Kind == TargetPromCR && hasGit == nil {
			v := HasGitIntegration(ctx.GetContext(), db, tenantID)
			hasGit = &v
		}
		if hasGit != nil {
			env.hasGit = *hasGit
		}
		t.Locator = buildLocator(t, opts)
		t.Muted = muted[t.ID]
		t.Operations = computeOperations(t, env)
		out = append(out, t)
	}
	return out, nil
}

// ResolveRuleTarget resolves one rule. It returns ErrRuleNotFound for an id
// outside the caller's tenant or readable accounts, before any provider call.
func ResolveRuleTarget(ctx *security.RequestContext, id string, opts ResolveOptions) (*RuleTarget, error) {
	targets, err := ResolveRuleTargets(ctx, []string{id}, opts)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, ErrRuleNotFound
	}
	return targets[0], nil
}

// GetRuleCapabilities is the eventrules_get_capabilities handler body.
func GetRuleCapabilities(ctx *security.RequestContext, ids []string) ([]RuleCapabilities, error) {
	targets, err := ResolveRuleTargets(ctx, ids, ResolveOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]RuleCapabilities, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Capabilities())
	}
	return out, nil
}

// requireOperation returns an error wrapping ErrOperationUnavailable when c is not available.
func requireOperation(c Capability, op string) error {
	if c.Available {
		return nil
	}
	return fmt.Errorf("%w: %s: %s", ErrOperationUnavailable, op, c.Reason)
}
