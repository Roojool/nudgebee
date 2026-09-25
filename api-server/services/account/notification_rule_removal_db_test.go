package account

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"

	"nudgebee/services/security"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Runs the mirror re-point and the notifications step of the delete plan
// against Postgres: a rule scoped only to the account goes with it, a rule over
// several accounts keeps the others and its mirror moves to one of them, and no
// rule scoped to accounts is ever left with a NULL mirror, which older code
// reads as every account.
func TestNotificationRuleAccountRemoval_DB(t *testing.T) {
	url := os.Getenv("APP_DATABASE_URL")
	if url == "" {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatal("REQUIRE_DB_TESTS is set but APP_DATABASE_URL is empty")
		}
		t.Skip("skipping: APP_DATABASE_URL is not set")
	}
	db, err := sqlx.Connect("postgres", url)
	if err != nil {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatalf("REQUIRE_DB_TESTS is set but the database is not accessible: %v", err)
		}
		t.Skipf("skipping: database not accessible: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	const schema = "zz_notification_rule_removal_test"
	const (
		acctA  = "00000000-0000-0000-0000-0000000000a1"
		acctB  = "00000000-0000-0000-0000-0000000000a2"
		acctC  = "00000000-0000-0000-0000-0000000000a3"
		only   = "00000000-0000-0000-0000-000000000001" // [A]
		multi  = "00000000-0000-0000-0000-000000000002" // [A, B], mirror A
		other  = "00000000-0000-0000-0000-000000000003" // [C, A], mirror C
		every  = "00000000-0000-0000-0000-000000000004" // all accounts
		tenant = "00000000-0000-0000-0000-00000000000a"
	)
	var group []tableDetails
	for _, g := range accountDataDeletePlan {
		if slices.ContainsFunc(g.tables, func(td tableDetails) bool { return td.name == "notification_rules" }) {
			group = g.tables
		}
	}
	require.NotEmpty(t, group, "the delete plan has no notification_rules step")

	setup := func(t *testing.T) {
		mustExec := func(q string) {
			_, e := db.Exec(q)
			require.NoError(t, e)
		}
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
		mustExec(`CREATE SCHEMA ` + schema)
		mustExec(`SET search_path TO ` + schema)
		mustExec(`CREATE TABLE cloud_accounts (id uuid PRIMARY KEY, account_name text NOT NULL)`)
		mustExec(`CREATE TABLE notification_channel_account_mappings (account_id uuid)`)
		mustExec(`CREATE TABLE notification_rules (id uuid PRIMARY KEY, scope_mode text NOT NULL, cluster text,
			account_id uuid REFERENCES cloud_accounts(id) ON DELETE CASCADE)`)
		mustExec(`CREATE TABLE notification_rule_accounts (
			rule_id uuid REFERENCES notification_rules(id) ON DELETE CASCADE,
			account_id uuid REFERENCES cloud_accounts(id) ON DELETE CASCADE,
			tenant_id uuid, PRIMARY KEY (rule_id, account_id))`)
		mustExec(`INSERT INTO cloud_accounts VALUES ('` + acctA + `', 'dev'), ('` + acctB + `', 'prod'), ('` + acctC + `', 'qa')`)
		mustExec(`INSERT INTO notification_rules VALUES
			('` + only + `', 'accounts', 'dev', '` + acctA + `'),
			('` + multi + `', 'accounts', 'dev', '` + acctA + `'),
			('` + other + `', 'accounts', 'qa', '` + acctC + `'),
			('` + every + `', 'all', NULL, NULL)`)
		mustExec(`INSERT INTO notification_rule_accounts VALUES
			('` + only + `', '` + acctA + `', '` + tenant + `'),
			('` + multi + `', '` + acctA + `', '` + tenant + `'), ('` + multi + `', '` + acctB + `', '` + tenant + `'),
			('` + other + `', '` + acctC + `', '` + tenant + `'), ('` + other + `', '` + acctA + `', '` + tenant + `')`)
	}
	ctx := security.NewRequestContext(context.Background(), &security.SecurityContext{}, slog.Default(), nil, nil)
	remove := func(t *testing.T, deleteAccountRow bool) {
		tx, err := db.Begin()
		require.NoError(t, err)
		require.NoError(t, repointNotificationRuleMirrors(ctx, tx, acctA))
		require.NoError(t, deleteTables(ctx, tx, group, acctA, "account_id"))
		if deleteAccountRow {
			_, err = tx.Exec(`DELETE FROM cloud_accounts WHERE id = $1`, acctA)
			require.NoError(t, err)
		}
		require.NoError(t, tx.Commit())
	}
	type ruleState struct {
		Id        string  `db:"id"`
		Mirror    *string `db:"mirror"`
		Cluster   *string `db:"cluster"`
		Accounts  string  `db:"accounts"`
		ScopeMode string  `db:"scope_mode"`
	}
	state := func(t *testing.T) map[string]ruleState {
		var rows []ruleState
		require.NoError(t, db.Select(&rows, `SELECT r.id::text AS id, r.account_id::text AS mirror, r.cluster, r.scope_mode,
			COALESCE(string_agg(a.account_id::text, ',' ORDER BY a.account_id), '') AS accounts
			FROM notification_rules r LEFT JOIN notification_rule_accounts a ON a.rule_id = r.id
			GROUP BY r.id, r.account_id, r.cluster, r.scope_mode`))
		out := map[string]ruleState{}
		for _, r := range rows {
			out[r.Id] = r
			if r.ScopeMode == "accounts" {
				assert.NotNil(t, r.Mirror, "rule %s is scoped to accounts but its mirror is NULL", r.Id)
			}
		}
		return out
	}
	str := func(s string) *string { return &s }

	for _, tc := range []struct {
		name             string
		deleteAccountRow bool
	}{{"account removed", true}, {"account cleaned", false}} {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			remove(t, tc.deleteAccountRow)
			rules := state(t)

			assert.NotContains(t, rules, only, "a rule scoped only to the account goes with it")
			assert.Equal(t, ruleState{Id: multi, Mirror: str(acctB), Cluster: str("prod"), Accounts: acctB, ScopeMode: "accounts"}, rules[multi])
			assert.Equal(t, ruleState{Id: other, Mirror: str(acctC), Cluster: str("qa"), Accounts: acctC, ScopeMode: "accounts"}, rules[other])
			assert.Equal(t, ruleState{Id: every, ScopeMode: "all"}, rules[every])
		})
	}
}
