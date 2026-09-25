package query

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"nudgebee/services/common"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRulesSQL runs a notification-rules query as ctx against a mock
// database and returns the SQL the engine sent.
func captureRulesSQL(t *testing.T, ctx *security.RequestContext, request QueryRequest) (string, QueryResponse) {
	t.Helper()
	var captured string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_ string, actual string) error {
		captured = actual
		return nil
	})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	database.RegisterDatabaseManagerHook(database.Metastore, func() (*database.DatabaseManager, error) {
		return &database.DatabaseManager{Db: sqlx.NewDb(db, "postgresql")}, nil
	})
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	resp, err := ExecuteQuery(ctx, request)
	require.NoError(t, err)
	return captured, resp
}

func rulesRequest() QueryRequest {
	return QueryRequest{Table: "admin_get_notification_rules_v2", Columns: fromStringColsToQueryCols([]string{"id"}), Limit: 10}
}

func TestNotificationRuleScope_AccountAdminGetsScopePredicate(t *testing.T) {
	ctx := scopedCtx(t, fmt.Sprintf(`{"TenantId":"t1","UserId":"u1","Roles":[%[1]q],"AccountIds":["acctA","acctB","acctC"],"ScopedEntityIds":{%[1]q:["acctA","acctB"]}}`, security.AUTH_ACCOUNT_ADMIN_ROLE))

	sql, resp := captureRulesSQL(t, ctx, rulesRequest())

	assert.Empty(t, resp.Errors)
	assert.Contains(t, sql, "notification_rules_v2.scope_mode = 'accounts'")
	assert.Contains(t, sql, "NOT IN ('acctA', 'acctB')")
	assert.Contains(t, sql, "t1", "the tenant filter still applies")
	assert.NotContains(t, sql, "account_id IN", "the scalar account_id restriction is replaced, not added")
}

func TestNotificationRuleScope_ScopedGrantUsesTheGrantedAccounts(t *testing.T) {
	ctx := scopedCtx(t, `{"TenantId":"t1","UserId":"u1","Roles":[],"AccountIds":["acctA","acctB"],
		"ScopedCustomPermissions":{"acctA":{"notifications:Read":true}}}`)

	sql, resp := captureRulesSQL(t, ctx, rulesRequest())

	assert.Empty(t, resp.Errors)
	assert.Contains(t, sql, "NOT IN ('acctA')")
	assert.NotContains(t, sql, "acctB")
}

func TestNotificationRuleScope_AggregateTableIsScopedToo(t *testing.T) {
	ctx := scopedCtx(t, fmt.Sprintf(`{"TenantId":"t1","UserId":"u1","Roles":[%[1]q],"AccountIds":["acctA"],"ScopedEntityIds":{%[1]q:["acctA"]}}`, security.AUTH_ACCOUNT_READ_ADMIN_ROLE))

	sql, resp := captureRulesSQL(t, ctx, QueryRequest{Table: "admin_get_notification_rules_grouping_v2", Limit: 10})

	assert.Empty(t, resp.Errors)
	assert.Contains(t, sql, "notification_rules_grouping.scope_mode = 'accounts'")
}

func TestNotificationRuleScope_TenantAdminIsNotRestricted(t *testing.T) {
	ctx := scopedCtx(t, fmt.Sprintf(`{"TenantId":"t1","UserId":"u1","Roles":[%q]}`, security.AUTH_TENANT_ADMIN_ROLE))

	sql, _ := captureRulesSQL(t, ctx, rulesRequest())

	assert.NotContains(t, sql, "scope_mode = 'accounts'")
}

func TestNotificationRuleScope_NoAccountsSeesNothing(t *testing.T) {
	assert.Equal(t, "FALSE", tableAccountScope(table_metadata["admin_get_notification_rules_v2"], nil))
}

func TestNotificationRuleScope_IdsAreQuoted(t *testing.T) {
	predicate := tableAccountScope(table_metadata["admin_get_notification_rules_v2"], []string{"a' OR '1'='1"})
	assert.Contains(t, predicate, "NOT IN ('a'' OR ''1''=''1')")
}

// The production decoder fills a field tagged json:"-" from a request key named
// "-", so the scope must never be reachable through the request. This decodes a
// hostile request the way the query RPC does and checks nothing it carries
// reaches the SQL.
func TestNotificationRuleScope_RequestCannotInjectSQL(t *testing.T) {
	raw := map[string]any{
		"table":   "admin_get_notification_rules_v2",
		"limit":   10,
		"columns": []any{map[string]any{"name": "id"}},
		"where": map[string]any{
			"-":       "1=1 OR TRUE",
			"_binary": map[string]any{"name": map[string]any{"_eq": "x"}},
		},
		"-": "1=1 OR TRUE",
	}
	var request QueryRequest
	require.NoError(t, common.UnmarshalMapToStruct(raw, &request))
	ctx := scopedCtx(t, fmt.Sprintf(`{"TenantId":"t1","UserId":"u1","Roles":[%[1]q],"AccountIds":["acctA"],"ScopedEntityIds":{%[1]q:["acctA"]}}`, security.AUTH_ACCOUNT_ADMIN_ROLE))

	sql, _ := captureRulesSQL(t, ctx, request)

	assert.NotContains(t, sql, "1=1 OR TRUE")
	assert.Contains(t, sql, "NOT IN ('acctA')")
}

// Runs the real derived table and scope predicate against Postgres: an account
// admin holding A and B sees rules over [A] and [A,B], not [A,C] or all
// accounts, and the account filter finds a rule by any of its accounts.
func TestNotificationRuleScope_DB(t *testing.T) {
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

	const schema = "zz_notification_rule_scope_test"
	mustExec := func(q string, args ...any) {
		_, e := db.Exec(q, args...)
		require.NoError(t, e)
	}
	drop := func() { _, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) }
	drop()
	t.Cleanup(drop)
	mustExec(`CREATE SCHEMA ` + schema)
	mustExec(`SET search_path TO ` + schema)
	mustExec(`CREATE TABLE users (id uuid PRIMARY KEY, display_name text)`)
	mustExec(`CREATE TABLE notification_rules (
		id uuid PRIMARY KEY, tenant_id uuid NOT NULL, account_id uuid, source text, created_at timestamp DEFAULT now(),
		cluster text, description text, aggregation_key text, expires_at timestamp, workload text, name text,
		namespace text, is_suppressed boolean, created_by uuid, severity text, delivery_mode text, frequency text,
		event_types json, scope_mode text NOT NULL, position integer, continue_matching boolean)`)
	mustExec(`CREATE TABLE notification_rule_mappings (id uuid PRIMARY KEY, rule_id uuid, platform text, channels json)`)
	mustExec(`CREATE TABLE notification_rule_accounts (rule_id uuid, account_id uuid, tenant_id uuid, PRIMARY KEY (rule_id, account_id))`)

	const (
		tenant = "00000000-0000-0000-0000-00000000000a"
		acctA  = "00000000-0000-0000-0000-0000000000a1"
		acctB  = "00000000-0000-0000-0000-0000000000a2"
		acctC  = "00000000-0000-0000-0000-0000000000a3"
	)
	rules := map[string][]string{
		"00000000-0000-0000-0000-000000000001": {acctA},
		"00000000-0000-0000-0000-000000000002": {acctA, acctB},
		"00000000-0000-0000-0000-000000000003": {acctA, acctC},
		"00000000-0000-0000-0000-000000000004": nil,
	}
	for id, accounts := range rules {
		mode := "accounts"
		if accounts == nil {
			mode = "all"
		}
		mustExec(`INSERT INTO notification_rules (id, tenant_id, name, source, scope_mode) VALUES ($1, $2, $3, 'troubleshoot', $4)`, id, tenant, "rule "+id, mode)
		for _, a := range accounts {
			mustExec(`INSERT INTO notification_rule_accounts VALUES ($1, $2, $3)`, id, a, tenant)
		}
	}
	database.RegisterDatabaseManagerHook(database.Metastore, func() (*database.DatabaseManager, error) {
		return &database.DatabaseManager{Db: db}, nil
	})
	ids := func(resp QueryResponse) []string {
		out := []string{}
		for _, row := range resp.Rows {
			out = append(out, fmt.Sprint(row["id"]))
		}
		sort.Strings(out)
		return out
	}

	accountAdmin := scopedCtx(t, fmt.Sprintf(`{"TenantId":%[1]q,"UserId":"u1","Roles":[%[2]q],"AccountIds":[%[3]q,%[4]q,%[5]q],"ScopedEntityIds":{%[2]q:[%[3]q,%[4]q]}}`, tenant, security.AUTH_ACCOUNT_ADMIN_ROLE, acctA, acctB, acctC))
	resp, err := ExecuteQuery(accountAdmin, rulesRequest())
	require.NoError(t, err)
	require.Empty(t, resp.Errors)
	assert.Equal(t, []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"}, ids(resp))

	tenantAdmin := scopedCtx(t, fmt.Sprintf(`{"TenantId":%q,"UserId":"u1","Roles":[%q]}`, tenant, security.AUTH_TENANT_ADMIN_ROLE))
	byAccount := rulesRequest()
	byAccount.Where = QueryWhereClause{Binary: BinaryWhereClause{"account_ids": {Contains: fmt.Sprintf(`[%q]`, acctB)}}}
	resp, err = ExecuteQuery(tenantAdmin, byAccount)
	require.NoError(t, err)
	require.Empty(t, resp.Errors)
	assert.Equal(t, []string{"00000000-0000-0000-0000-000000000002"}, ids(resp))
}
