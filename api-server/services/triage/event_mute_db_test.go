package triage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"nudgebee/services/eventrule"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Which rows a mute owns is decided in SQL over jsonb tags and nullable match
// columns, where a wrong predicate silently re-purposes or disables another mute.
// Named _DB so the "PR lifecycle sweeps (real Postgres)" job's -run '_DB$' filter
// picks it up. Skipped locally unless a database is configured.
func muteTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("APP_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_POSTGRES_DSN")
	}
	if dsn == "" {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatal("REQUIRE_DB_TESTS is set but neither APP_DATABASE_URL nor TEST_POSTGRES_DSN is")
		}
		t.Skip("no database configured; export APP_DATABASE_URL to run this suite")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	// One connection, so the search_path set below holds for every later statement.
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("event_mute_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	_, err = db.Exec(fmt.Sprintf(`CREATE SCHEMA %s; SET search_path TO %s`, schema, schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
		_ = db.Close()
	})
	// Mirrors the real event_triage_rules columns the insert and the lookups use.
	_, err = db.Exec(`CREATE TABLE event_triage_rules (
		id uuid PRIMARY KEY, tenant_id uuid, account_id uuid, rule_type text NOT NULL,
		match_source text, match_alertname text, match_namespace text, match_service text,
		match_fingerprint text, match_labels jsonb, match_priority text, match_finding_type text,
		action text NOT NULL, action_value jsonb, priority int NOT NULL DEFAULT 100,
		is_editable bool, can_override bool, enabled bool NOT NULL DEFAULT true,
		effective_from timestamp, effective_until timestamp,
		name text, description text, reason text,
		created_by uuid, updated_by uuid, created_at timestamp, updated_at timestamp,
		apply_to_existing bool
	)`)
	require.NoError(t, err)
	return db
}

type muteRow struct {
	Source    string     `db:"match_source"`
	Namespace *string    `db:"match_namespace"`
	Service   *string    `db:"match_service"`
	Enabled   bool       `db:"enabled"`
	Scope     *string    `db:"scope"`
	Until     *time.Time `db:"effective_until"`
}

func muteRows(t *testing.T, db *sqlx.DB) []muteRow {
	t.Helper()
	var rows []muteRow
	require.NoError(t, db.Select(&rows, `
		SELECT match_source, match_namespace, match_service, enabled, effective_until,
		       action_value->>'`+eventrule.MuteScopeKey+`' AS scope
		FROM event_triage_rules ORDER BY match_source, match_namespace NULLS FIRST`))
	return rows
}

const (
	muteTenant  = "11111111-1111-1111-1111-111111111111"
	muteAccount = "22222222-2222-2222-2222-222222222222"
	muteUser    = "33333333-3333-3333-3333-333333333333"
	muteRuleID  = "44444444-4444-4444-4444-444444444444"
)

// A rule-wide mute (Alert Manager, or "every alert" on the event) after a
// workload mute of the same rule must add its own row, not re-purpose the
// workload row; and unmuting one scope from the event leaves the other alone.
func TestMuteScopesKeepTheirOwnRows_DB(t *testing.T) {
	db := muteTestDB(t)
	ctx := context.Background()
	day := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	workload := muteSpec{tenantID: muteTenant, accountID: muteAccount, ruleID: muteRuleID, alert: "KubePodCrashLooping",
		scope: eventrule.MuteScopeWorkload, namespace: "demo", owner: "fraud-detection", sources: []string{"prometheus"}}
	require.NoError(t, upsertMute(ctx, db, workload, muteUser, &day))
	require.NoError(t, muteRule(ctx, db, muteTenant, muteAccount, muteUser, muteRuleID, "KubePodCrashLooping", []string{"prometheus"}, nil))

	rows := muteRows(t, db)
	require.Len(t, rows, 2)
	assert.Nil(t, rows[0].Namespace, "rule-wide row")
	assert.Equal(t, "rule", *rows[0].Scope)
	assert.Nil(t, rows[0].Until)
	require.NotNil(t, rows[1].Namespace, "workload row kept its match")
	assert.Equal(t, "demo", *rows[1].Namespace)
	assert.Equal(t, "^fraud-detection$", *rows[1].Service)
	assert.WithinDuration(t, day, *rows[1].Until, time.Second)

	// Muting the same workload again reuses its row and moves the end.
	week := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Second)
	require.NoError(t, upsertMute(ctx, db, workload, muteUser, &week))
	rows = muteRows(t, db)
	require.Len(t, rows, 2, "no duplicate row")
	assert.WithinDuration(t, week, *rows[1].Until, time.Second)

	// Unmuting the rule-wide mute from the event leaves the workload mute on.
	rule := workload
	rule.scope, rule.namespace, rule.owner = eventrule.MuteScopeRule, "", ""
	require.NoError(t, disableMute(ctx, db, rule, muteUser))
	rows = muteRows(t, db)
	assert.False(t, rows[0].Enabled)
	assert.True(t, rows[1].Enabled)

	// A workload mute of another workload is its own row; unmuting it leaves this one.
	other := workload
	other.owner = "ad"
	require.NoError(t, upsertMute(ctx, db, other, muteUser, &day))
	require.NoError(t, disableMute(ctx, db, other, muteUser))
	var enabled []string
	require.NoError(t, db.Select(&enabled, `SELECT match_service FROM event_triage_rules WHERE enabled`))
	assert.Equal(t, []string{"^fraud-detection$"}, enabled)

	// Unmute in Alert Manager unmutes the rule everywhere, any scope.
	require.NoError(t, unmuteRule(ctx, db, muteTenant, muteAccount, muteUser, muteRuleID))
	require.NoError(t, db.Select(&enabled, `SELECT match_service FROM event_triage_rules WHERE enabled`))
	assert.Empty(t, enabled)
}

// Alert Manager mutes made before scopes existed carry only the rule marker; the
// rule-wide mute must still find and reuse them.
func TestRuleMuteReusesPreScopeRow_DB(t *testing.T) {
	db := muteTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO event_triage_rules (id, tenant_id, account_id, rule_type, match_source, match_alertname, action, action_value, enabled)
		VALUES ('55555555-5555-5555-5555-555555555555', $1, $2, 'suppression', 'prometheus', '^X$', 'suppress', $3, false)`,
		muteTenant, muteAccount, `{"`+eventrule.MuteMarkerKey+`":"`+muteRuleID+`"}`)
	require.NoError(t, err)

	require.NoError(t, muteRule(ctx, db, muteTenant, muteAccount, muteUser, muteRuleID, "X", []string{"prometheus"}, nil))
	rows := muteRows(t, db)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Enabled)
	assert.Equal(t, "rule", *rows[0].Scope, "tagged with its scope on reuse")
}

// An event that maps to no alert rule is muted by its alert key; muting or
// unmuting it never touches another alert's mute.
func TestMuteWithoutRuleIsKeyedByAlert_DB(t *testing.T) {
	db := muteTestDB(t)
	ctx := context.Background()
	crash := muteSpec{tenantID: muteTenant, accountID: muteAccount, alert: "report_crash_loop",
		scope: eventrule.MuteScopeRule, sources: []string{"kubernetes_api_server"}}
	oom := crash
	oom.alert = "pod_oom_killer_enricher"
	require.NoError(t, upsertMute(ctx, db, crash, muteUser, nil))
	require.NoError(t, upsertMute(ctx, db, oom, muteUser, nil))
	require.NoError(t, upsertMute(ctx, db, crash, muteUser, nil))
	require.Len(t, muteRows(t, db), 2)

	require.NoError(t, disableMute(ctx, db, crash, muteUser))
	var enabled []string
	require.NoError(t, db.Select(&enabled, `SELECT match_alertname FROM event_triage_rules WHERE enabled`))
	assert.Equal(t, []string{"^pod_oom_killer_enricher$"}, enabled)
}
