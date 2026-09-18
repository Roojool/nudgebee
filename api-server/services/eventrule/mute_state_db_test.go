package eventrule

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Alert Manager's Muted / "muted for N workloads" come from one query over jsonb
// tags and effective_until. Named _DB so the "PR lifecycle sweeps (real
// Postgres)" job picks it up; skipped locally unless a database is configured.
func TestLoadMuteStates_DB(t *testing.T) {
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
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("mute_state_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	_, err = db.Exec(fmt.Sprintf(`CREATE SCHEMA %s; SET search_path TO %s`, schema, schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
		_ = db.Close()
	})
	_, err = db.Exec(`CREATE TABLE event_triage_rules (
		id serial PRIMARY KEY, tenant_id uuid, enabled bool NOT NULL,
		effective_until timestamp, action_value jsonb,
		match_namespace text, match_service text)`)
	require.NoError(t, err)

	const tenant = "11111111-1111-1111-1111-111111111111"
	add := func(enabled bool, until string, actionValue, ns, svc string) {
		t.Helper()
		var u, n, s any
		if until != "" {
			u = until
		}
		if ns != "" {
			n, s = ns, svc
		}
		_, err := db.Exec(`INSERT INTO event_triage_rules (tenant_id, enabled, effective_until, action_value, match_namespace, match_service)
			VALUES ($1, $2, $3::timestamp, $4, $5, $6)`, tenant, enabled, u, actionValue, n, s)
		require.NoError(t, err)
	}
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	// r-old: an Alert Manager mute from before scopes (marker only) is rule-wide.
	add(true, "", `{"mute_event_rule_id":"r-old"}`, "", "")
	// r-exp: an expired rule-wide mute is not muted.
	add(true, past, `{"mute_event_rule_id":"r-exp","mute_scope":"rule"}`, "", "")
	// r-wl: two workloads (one on two sources), one expired workload, one disabled.
	add(true, future, `{"mute_event_rule_id":"r-wl","mute_scope":"workload"}`, "demo", "^ad$")
	add(true, future, `{"mute_event_rule_id":"r-wl","mute_scope":"workload"}`, "demo", "^ad$")
	add(true, "", `{"mute_event_rule_id":"r-wl","mute_scope":"workload"}`, "demo", "^cart$")
	add(true, past, `{"mute_event_rule_id":"r-wl","mute_scope":"workload"}`, "demo", "^quote$")
	add(false, "", `{"mute_event_rule_id":"r-wl","mute_scope":"rule"}`, "", "")

	got, err := loadMuteStates(context.Background(), db, tenant, []string{"r-old", "r-exp", "r-wl", "r-none"})
	require.NoError(t, err)
	assert.Equal(t, muteState{Rule: true}, got["r-old"])
	assert.Equal(t, muteState{}, got["r-exp"])
	assert.Equal(t, muteState{Workloads: 2}, got["r-wl"])
	assert.Equal(t, muteState{}, got["r-none"])
}
