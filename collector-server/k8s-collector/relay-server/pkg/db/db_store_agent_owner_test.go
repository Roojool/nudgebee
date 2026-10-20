package db

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockStore(t *testing.T) (*pgStore, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })
	return &pgStore{db: sqlx.NewDb(mockDB, "postgres")}, mock
}

func q(s string) string { return regexp.QuoteMeta(s) }

// expectTenantByAgent, expectIntegrationUpsert and expectDualModeConfig script
// the per-datasource statements that do not concern ownership.
func expectTenantByAgent(mock sqlmock.Sqlmock, agentID string) {
	mock.ExpectQuery(q(`SELECT tenant FROM agent WHERE id = $1`)).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant"}).AddRow("tenant-1"))
}

func expectIntegrationUpsert(mock sqlmock.Sqlmock, intType, name, id string) {
	mock.ExpectQuery(q(`INSERT INTO integrations (id, tenant_id, type, source, name`)).
		WithArgs("tenant-1", intType, name).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectExec(q(`INSERT INTO integrations_cloud_accounts`)).
		WithArgs(id, "acct-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectDualModeConfig(mock sqlmock.Sqlmock, id string) {
	for i := 0; i < 3; i++ {
		mock.ExpectExec(q(`INSERT INTO integration_config_values`)).
			WithArgs(id, sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func expectRelease(mock sqlmock.Sqlmock, id, agentID string, remaining, onAccount int) {
	mock.ExpectBegin()
	mock.ExpectQuery(q(`SELECT id::text FROM integrations WHERE id = $1 FOR UPDATE`)).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectExec(q(`DELETE FROM integration_config_values WHERE integration_id = $1 AND name = $2`)).
		WithArgs(id, "agent_owner:"+agentID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(`AS on_account`)).
		WithArgs(id, "acct-1").
		WillReturnRows(sqlmock.NewRows([]string{"remaining", "on_account"}).AddRow(remaining, onAccount))
}

// D1: agent A's inventory claims what it reports, and stale cleanup lists only
// integrations A claims ($3) — never rows claimed only by agent B. A stale
// integration nobody else claims is deleted.
func TestUpsertAgentDatasources_ClaimsAndDeletesUnclaimedStale(t *testing.T) {
	store, mock := newMockStore(t)

	expectTenantByAgent(mock, "agent-a")
	expectIntegrationUpsert(mock, "ssh", "fleet-ssh", "int-1")
	mock.ExpectQuery(q(`ON CONFLICT (integration_id, name) DO NOTHING`)).
		WithArgs("int-1", "agent_owner:agent-a", "agent-a").
		WillReturnRows(sqlmock.NewRows([]string{"others"}).AddRow("")) // new claim, no one else
	expectDualModeConfig(mock, "int-1")

	mock.ExpectQuery(q(`SELECT COALESCE(COUNT(*) = 1 AND BOOL_AND(id::text = $2), false)`)).
		WithArgs("acct-1", "agent-a").
		WillReturnRows(sqlmock.NewRows([]string{"sole"}).AddRow(false))
	mock.ExpectQuery(q(`WHERE c.integration_id = i.id AND c.name = $3`)).
		WithArgs("acct-1", "tenant-1", "agent_owner:agent-a", false).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "type"}).
			AddRow("int-1", "fleet-ssh", "ssh").           // still reported: kept
			AddRow("int-9", "old-postgres", "postgresql")) // A's, no longer reported
	expectRelease(mock, "int-9", "agent-a", 0, 0)
	mock.ExpectExec(q(`DELETE FROM integration_config_values WHERE integration_id = $1`)).
		WithArgs("int-9").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(q(`DELETE FROM integrations WHERE id = $1`)).
		WithArgs("int-9").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := store.UpsertAgentDatasources(context.Background(), "acct-1", "proxy", "agent-a", []AgentDatasource{
		{ID: "local:fleet:ssh", Type: "ssh", ProxyType: "ssh-proxy", Name: "fleet-ssh"},
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Review finding: A and B both report `pg` (one shared row). A drops it. Only
// A's claim goes; B's claim, the row, and — since B is on the same account —
// the account link all stay.
func TestUpsertAgentDatasources_SharedRowKeepsOtherAgentsClaim(t *testing.T) {
	store, mock := newMockStore(t)

	expectTenantByAgent(mock, "agent-a")
	expectIntegrationUpsert(mock, "mcp", "tools", "int-2")
	mock.ExpectQuery(q(`ON CONFLICT (integration_id, name) DO NOTHING`)).
		WithArgs("int-2", "agent_owner:agent-a", "agent-a").
		WillReturnRows(sqlmock.NewRows([]string{"others"})) // already claimed: no row
	expectDualModeConfig(mock, "int-2")

	mock.ExpectQuery(q(`BOOL_AND`)).
		WillReturnRows(sqlmock.NewRows([]string{"sole"}).AddRow(false))
	mock.ExpectQuery(q(`c.name = $3`)).
		WithArgs("acct-1", "tenant-1", "agent_owner:agent-a", false).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "type"}).AddRow("int-pg", "pg", "postgresql"))
	expectRelease(mock, "int-pg", "agent-a", 1, 1) // B still claims it, on this account
	mock.ExpectCommit()

	err := store.UpsertAgentDatasources(context.Background(), "acct-1", "proxy", "agent-a", []AgentDatasource{
		{ID: "local:tools", ProxyType: "mcp-proxy", Name: "tools"},
	})
	require.NoError(t, err)
	// No DELETE of the integration or its account link was expected.
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the remaining claimant is on another account, only this account's
// 'own' link is removed; the row survives for the other agent.
func TestReleaseAgentDatasource_DropsOnlyThisAccountsLinkWhenClaimedElsewhere(t *testing.T) {
	store, mock := newMockStore(t)

	expectRelease(mock, "int-pg", "agent-a", 1, 0)
	mock.ExpectExec(q(`DELETE FROM integrations_cloud_accounts`)).
		WithArgs("int-pg", "acct-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	deleted, err := store.releaseAgentDatasource(context.Background(), "int-pg", "acct-1", "agent-a")
	require.NoError(t, err)
	assert.False(t, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Review finding 2: the sole proxy agent on an account adopts unclaimed legacy
// rows for cleanup ($4 = true), so rows written before claims existed are
// still removed when it stops reporting them.
func TestUpsertAgentDatasources_SoleProxyAgentCleansUnclaimedLegacyRows(t *testing.T) {
	store, mock := newMockStore(t)

	expectTenantByAgent(mock, "agent-a")
	expectIntegrationUpsert(mock, "mcp", "tools", "int-2")
	mock.ExpectQuery(q(`ON CONFLICT (integration_id, name) DO NOTHING`)).
		WillReturnRows(sqlmock.NewRows([]string{"others"}))
	expectDualModeConfig(mock, "int-2")

	mock.ExpectQuery(q(`BOOL_AND`)).
		WithArgs("acct-1", "agent-a").
		WillReturnRows(sqlmock.NewRows([]string{"sole"}).AddRow(true))
	mock.ExpectQuery(q(`c.name = $3`)).
		WithArgs("acct-1", "tenant-1", "agent_owner:agent-a", true).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "type"}).AddRow("int-legacy", "old", "postgresql"))
	mock.ExpectBegin()
	mock.ExpectQuery(q(`FOR UPDATE`)).WithArgs("int-legacy").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("int-legacy"))
	mock.ExpectExec(q(`AND name = $2`)).WithArgs("int-legacy", "agent_owner:agent-a").
		WillReturnResult(sqlmock.NewResult(0, 0)) // it never had a claim
	mock.ExpectQuery(q(`AS on_account`)).
		WillReturnRows(sqlmock.NewRows([]string{"remaining", "on_account"}).AddRow(0, 0))
	mock.ExpectExec(q(`DELETE FROM integration_config_values WHERE integration_id = $1`)).
		WithArgs("int-legacy").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q(`DELETE FROM integrations WHERE id = $1`)).
		WithArgs("int-legacy").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := store.UpsertAgentDatasources(context.Background(), "acct-1", "proxy", "agent-a", []AgentDatasource{
		{ID: "local:tools", ProxyType: "mcp-proxy", Name: "tools"},
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Legacy adoption must only ever reach rows the relay itself created, never
// k8s-collector's own source='agent' integrations, and must be gated on the
// sole-agent flag.
func TestStaleAgentDatasourcesSQL_LegacyAdoptionIsGuarded(t *testing.T) {
	assert.Contains(t, staleAgentDatasourcesSQL, "$4::boolean")
	assert.Contains(t, staleAgentDatasourcesSQL, "starts_with(c.name::text, 'agent_owner:')")
	assert.Contains(t, staleAgentDatasourcesSQL, "d.name = 'datasource_key'")
	assert.Contains(t, soleProxyAgentSQL, `"type" = 'proxy'`)
}

// Without the reporting agent's id there is no safe scope for cleanup: it
// must neither claim ownership nor delete anything.
func TestUpsertAgentDatasources_WithoutAgentIDSkipsOwnershipAndCleanup(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectQuery(q(`SELECT tenant FROM agent WHERE cloud_account_id = $1 AND "type" = $2`)).
		WithArgs("acct-1", "proxy").
		WillReturnRows(sqlmock.NewRows([]string{"tenant"}).AddRow("tenant-1"))
	mock.ExpectQuery(q(`INSERT INTO integrations (id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("int-3"))
	mock.ExpectExec(q(`INSERT INTO integrations_cloud_accounts`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for i := 0; i < 3; i++ {
		mock.ExpectExec(q(`INSERT INTO integration_config_values`)).WillReturnResult(sqlmock.NewResult(0, 1))
	}

	err := store.UpsertAgentDatasources(context.Background(), "acct-1", "proxy", "", []AgentDatasource{
		{ID: "local:pg", Type: "postgresql", ProxyType: "db-proxy", Name: "pg"},
	})
	require.NoError(t, err)
	// Any unexpected claim/stale/delete query would have failed above.
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDatasourceAgentID(t *testing.T) {
	t.Run("single owner is returned and cached", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(q(`WHERE dk.name = 'datasource_key' AND dk.value = $2`)).
			WithArgs("acct-1", "local:fleet:ssh").
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("agent-a"))

		for i := 0; i < 2; i++ { // second call must be served from cache
			owner, err := store.GetDatasourceAgentID(context.Background(), "acct-1", "local:fleet:ssh")
			require.NoError(t, err)
			assert.Equal(t, "agent-a", owner)
		}
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("ambiguous owner resolves to unknown", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(q(`LIMIT 2`)).
			WithArgs("acct-1", "local:pg").
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("agent-a").AddRow("agent-b"))

		owner, err := store.GetDatasourceAgentID(context.Background(), "acct-1", "local:pg")
		require.NoError(t, err)
		assert.Empty(t, owner)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("legacy row without agent_id resolves to unknown", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(q(`LIMIT 2`)).
			WithArgs("acct-1", "local:legacy").
			WillReturnRows(sqlmock.NewRows([]string{"value"}))

		owner, err := store.GetDatasourceAgentID(context.Background(), "acct-1", "local:legacy")
		require.NoError(t, err)
		assert.Empty(t, owner)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// The owner lookup must only accept an agent that belongs to the same
// account, so a stray config value cannot route a request to another
// account's agent.
func TestDatasourceOwnerSQL_RequiresOwnerOnSameAccount(t *testing.T) {
	assert.Contains(t, datasourceOwnerSQL, `JOIN agent a ON a.id::text = c.value AND a.cloud_account_id = $1 AND a."type" = 'proxy'`)
}

func TestUpdateDatasourceMetadata_ReportsNotFoundAndChanges(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(q(`SELECT tenant FROM agent WHERE cloud_account_id = $1 AND "type" = $2`)).
		WithArgs("acct-1", "proxy").
		WillReturnRows(sqlmock.NewRows([]string{"tenant"}).AddRow("tenant-1"))
	mock.ExpectQuery(q(`SELECT (SELECT count(*) FROM target) AS matched, (SELECT count(*) FROM upd) AS changed`)).
		WithArgs(`{"version":"1"}`, "fleet-ssh", "tenant-1", "local:fleet-ssh").
		WillReturnRows(sqlmock.NewRows([]string{"matched", "changed"}).AddRow(1, 1))

	notFound, changed, err := store.UpdateDatasourceMetadata(context.Background(), "acct-1", "proxy",
		map[string]map[string]any{"local:fleet-ssh": {"version": "1"}})
	require.NoError(t, err)
	assert.Empty(t, notFound)
	assert.True(t, changed)

	mock.ExpectQuery(q(`SELECT tenant FROM agent`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant"}).AddRow("tenant-1"))
	mock.ExpectQuery(q(`AS matched`)).
		WillReturnRows(sqlmock.NewRows([]string{"matched", "changed"}).AddRow(0, 0))

	notFound, changed, err = store.UpdateDatasourceMetadata(context.Background(), "acct-1", "proxy",
		map[string]map[string]any{"local:missing": {"version": "1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"local:missing"}, notFound)
	assert.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

// The discovery ssh sibling's ID ("local:fleet:ssh") does not reduce to its
// name ("fleet-ssh"), so labels must be matched by datasource_key; name-only
// matching silently dropped its mode/allowed_hosts.
func TestUpdateDatasourceLabelsSQL_MatchesByDatasourceKey(t *testing.T) {
	assert.Contains(t, updateDatasourceLabelsSQL, "v.name = 'datasource_key' AND v.value = $4")
	assert.Contains(t, updateDatasourceLabelsSQL, "OR i.name = $2")
}

func TestUpdateDatasourceMetadata_PassesDatasourceIDForKeyMatch(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(q(`SELECT tenant FROM agent`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant"}).AddRow("tenant-1"))
	mock.ExpectQuery(q(`AS matched`)).
		WithArgs(`{"mode":"dynamic"}`, "fleet:ssh", "tenant-1", "local:fleet:ssh").
		WillReturnRows(sqlmock.NewRows([]string{"matched", "changed"}).AddRow(1, 1))

	notFound, changed, err := store.UpdateDatasourceMetadata(context.Background(), "acct-1", "proxy",
		map[string]map[string]any{"local:fleet:ssh": {"mode": "dynamic"}})
	require.NoError(t, err)
	assert.Empty(t, notFound)
	assert.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A claim change must drop this replica's cached routing answers for that
// account only.
func TestForgetAccountOwners(t *testing.T) {
	store := &pgStore{}
	store.ownerCache.Store(ownerCacheKey("acct-1", "local:a"), ownerCacheEntry{AgentID: "agent-a"})
	store.ownerCache.Store(ownerCacheKey("acct-1", "local:b"), ownerCacheEntry{AgentID: "agent-b"})
	store.ownerCache.Store(ownerCacheKey("acct-10", "local:a"), ownerCacheEntry{AgentID: "agent-x"})

	store.forgetAccountOwners("acct-1")

	var left []string
	store.ownerCache.Range(func(k, _ any) bool { left = append(left, k.(string)); return true })
	assert.Equal(t, []string{ownerCacheKey("acct-10", "local:a")}, left)
}
