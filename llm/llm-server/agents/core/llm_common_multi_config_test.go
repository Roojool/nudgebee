package core

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"nudgebee/llm/common"
)

// mockLLMConfigDB returns a DatabaseManager backed by sqlmock so the LLM
// integration-config queries run against canned rows. The default sqlmock
// matcher is regexp, so query expectations match on substrings.
func mockLLMConfigDB(t *testing.T) (*common.DatabaseManager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &common.DatabaseManager{Db: sqlx.NewDb(db, "postgresql")}, mock
}

// expectSelect queues the integration-selection query (step 1), returning one
// row per enabled LLM integration linked to the account. None of them is the
// tenant default; use expectSelectWithTenantDefault for that.
func expectSelect(mock sqlmock.Sqlmock, integrations ...[2]any) {
	rows := make([][3]any, 0, len(integrations))
	for _, integ := range integrations {
		rows = append(rows, [3]any{integ[0], integ[1], false})
	}
	expectSelectWithTenantDefault(mock, rows...)
}

// expectSelectWithTenantDefault is expectSelect with the tenant_default column
// (id, account-link flag, tenant default) set per row.
func expectSelectWithTenantDefault(mock sqlmock.Sqlmock, integrations ...[3]any) {
	rows := sqlmock.NewRows([]string{"id", "default_llm_provider", "tenant_default"})
	for _, integ := range integrations {
		rows.AddRow(integ[0], integ[1], integ[2])
	}
	mock.ExpectQuery("integrations_cloud_accounts").WillReturnRows(rows)
}

// expectConfig queues the config-value query (step 2) for a single integration.
func expectConfig(mock sqlmock.Sqlmock, integrationId string, kv ...[2]string) {
	rows := sqlmock.NewRows([]string{"id", "name", "value", "is_encrypted"})
	for _, pair := range kv {
		rows.AddRow(integrationId, pair[0], pair[1], false)
	}
	mock.ExpectQuery("integration_config_values").WillReturnRows(rows)
}

// The flagged config wins, and its credential is the one returned whole — no
// values from the unflagged sibling leak into it.
func TestFetchLLMIntegrationConfigByAccount_PrefersFlaggedDefault(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelect(mock, [2]any{"int-a", false}, [2]any{"int-b", true})
	expectConfig(mock, "int-b",
		[2]string{"llm_provider", "huggingface"},
		[2]string{"llm_provider_api_key", "key-hf"},
		[2]string{"llm_provider_api_endpoint", "https://example.endpoints.huggingface.cloud"})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-multi")

	require.NoError(t, err)
	require.Equal(t, "huggingface", cfg["llm_provider"])
	require.Equal(t, "key-hf", cfg["llm_provider_api_key"])
	require.NotContains(t, cfg, "llm_model_name", "keys from the non-default config must not appear")
}

// A single UNFLAGGED integration resolves as the account's provider: linking
// exactly one provider to an account is the operator's choice for it, so the
// default checkbox only matters once an account has several.
func TestFetchLLMIntegrationConfigByAccount_SingleUnflaggedConfigResolves(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelect(mock, [2]any{"int-a", false})
	expectConfig(mock, "int-a",
		[2]string{"llm_provider", "openai"},
		[2]string{"llm_provider_api_key", "key-openai"})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-single")

	require.NoError(t, err)
	require.Equal(t, "openai", cfg["llm_provider"])
	require.Equal(t, "key-openai", cfg["llm_provider_api_key"])
	require.NoError(t, mock.ExpectationsWereMet())
}

// The flagged single config keeps resolving exactly as before.
func TestFetchLLMIntegrationConfigByAccount_SingleFlaggedConfigResolves(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelect(mock, [2]any{"int-a", true})
	expectConfig(mock, "int-a",
		[2]string{"llm_provider", "googleai"},
		[2]string{"llm_model_name", "gemini-3-flash-preview"},
		[2]string{"llm_provider_api_key", "key-google"})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-single")

	require.NoError(t, err)
	require.Equal(t, "googleai", cfg["llm_provider"])
	require.Equal(t, "gemini-3-flash-preview", cfg["llm_model_name"])
	require.Equal(t, "key-google", cfg["llm_provider_api_key"])
}

// Several configs and no default is a legitimate operator choice — it means
// "resolve me from ENV" — so it must be a quiet (nil, nil), not an error, and
// must not read any config values at all.
func TestFetchLLMIntegrationConfigByAccount_MultipleWithNoDefaultFallsThrough(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelect(mock, [2]any{"int-a", false}, [2]any{"int-b", false})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-no-default")

	require.NoError(t, err, "no DB default means ENV, which is a normal state")
	require.Nil(t, cfg)
	require.NoError(t, mock.ExpectationsWereMet(), "config values must not be read when no integration is chosen")
}

// Several unflagged links where one is the tenant default resolve to it: the
// integrations list already shows that provider as "Default" for the account.
func TestFetchLLMIntegrationConfigByAccount_MultipleUnflaggedPrefersTenantDefault(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelectWithTenantDefault(mock, [3]any{"int-a", false, false}, [3]any{"int-b", false, true})
	expectConfig(mock, "int-b",
		[2]string{"llm_provider", "googleai"},
		[2]string{"llm_provider_api_key", "key-google"})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-tenant-default")

	require.NoError(t, err)
	require.Equal(t, "googleai", cfg["llm_provider"])
	require.NoError(t, mock.ExpectationsWereMet())
}

// The account's own flag still beats the tenant default, so changing the
// tenant default never re-routes an account that picked a provider.
func TestFetchLLMIntegrationConfigByAccount_AccountFlagBeatsTenantDefault(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelectWithTenantDefault(mock, [3]any{"int-a", true, false}, [3]any{"int-b", false, true})
	expectConfig(mock, "int-a",
		[2]string{"llm_provider", "openai"},
		[2]string{"llm_provider_api_key", "key-openai"})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-own-default")

	require.NoError(t, err)
	require.Equal(t, "openai", cfg["llm_provider"])
	require.NoError(t, mock.ExpectationsWereMet())
}

// Two rows flagged default can only happen if something raced the exclusivity
// update. There is no right answer, so resolution must not guess.
func TestFetchLLMIntegrationConfigByAccount_MultipleDefaultsFallsThrough(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelect(mock, [2]any{"int-a", true}, [2]any{"int-b", true})

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-two-defaults")

	require.NoError(t, err)
	require.Nil(t, cfg)
	require.NoError(t, mock.ExpectationsWereMet())
}

// An account with no LLM integration reads nothing further and returns
// (nil, nil) so the caller falls through to the tenant rung and then ENV.
func TestFetchLLMIntegrationConfigByAccount_NoIntegrations(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	expectSelect(mock)

	cfg, err := fetchLLMIntegrationConfigByAccount(nil, dbManager, "acct-none")

	require.NoError(t, err, "no config is a normal state, not an error")
	require.Nil(t, cfg)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Defence in depth for the reader itself. Selection above should mean a result
// set never spans two integrations, but if a future caller hands it a wider
// query, collapsing the rows would invent a credential nobody configured:
// integration A's api key paired with integration B's endpoint and api type,
// pointing a Google key at a HuggingFace endpoint. Refusing sends the caller to
// ENV instead; guessing would send real requests to the wrong provider.
func TestExecLLMIntegrationConfigQuery_RefusesToBlendTwoIntegrations(t *testing.T) {
	dbManager, mock := mockLLMConfigDB(t)

	rows := sqlmock.NewRows([]string{"id", "name", "value", "is_encrypted"}).
		AddRow("int-a", "llm_provider", "googleai", false).
		AddRow("int-a", "llm_provider_api_key", "key-google", false).
		AddRow("int-b", "llm_provider", "huggingface", false).
		AddRow("int-b", "llm_provider_api_endpoint", "https://example.endpoints.huggingface.cloud", false)
	mock.ExpectQuery("integration_config_values").WillReturnRows(rows)

	// Mirrors the real reader's query. sqlmock never parses this, but a query
	// that wouldn't run against Postgres would misrepresent what is under test.
	cfg, err := execLLMIntegrationConfigQuery(nil, dbManager,
		`SELECT i.id, icv.name, icv.value, icv.is_encrypted FROM integrations i
		 JOIN integration_config_values icv ON i.id = icv.integration_id`,
		map[string]any{}, "acct-blend")

	require.ErrorIs(t, err, ErrAmbiguousLLMConfig)
	require.Nil(t, cfg, "no config may be returned when the result set is ambiguous")
}
