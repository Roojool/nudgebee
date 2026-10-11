package core

import (
	"regexp"
	"testing"

	"nudgebee/services/common"
	"nudgebee/services/config"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The integration form sends a secret the user left masked back as the stored
// ciphertext, flagged is_encrypted. Decrypting that client-supplied ciphertext made
// every endpoint taking config values a decryption oracle (#39639): one global key,
// no binding to tenant or integration. These tests pin the replacement — the flag is
// only a marker, and the secret comes from the caller's own saved row.

var matchStoredConfigValues = regexp.MustCompile(`(?s)SELECT icv\.name::text, COALESCE\(icv\.value, ''\)::text, COALESCE\(icv\.is_encrypted, false\).*FROM integration_config_values icv.*JOIN integrations i ON i\.id = icv\.integration_id.*i\.tenant_id = \$2`)

func withTestEncryptionKey(t *testing.T) {
	t.Helper()
	orig := config.Config.NudgebeeEncryptionKey
	config.Config.NudgebeeEncryptionKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	t.Cleanup(func() { config.Config.NudgebeeEncryptionKey = orig })
}

func mustEncrypt(t *testing.T, plaintext string) string {
	t.Helper()
	ciphertext, err := common.Encrypt(plaintext)
	require.NoError(t, err)
	return ciphertext
}

func storedRows(rows ...[3]any) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"name", "value", "is_encrypted"})
	for _, row := range rows {
		r.AddRow(row[0], row[1], row[2])
	}
	return r
}

func TestResolveKeptSecrets(t *testing.T) {
	withTestEncryptionKey(t)

	t.Run("nothing flagged passes through without a query", func(t *testing.T) {
		ctx := ctxForTenant(t, testTenant)
		in := []IntegrationConfigValue{{Name: "url", Value: "https://es.example:9200"}, {Name: "password", Value: "typed"}}

		got, err := ResolveKeptSecrets(ctx, "int-1", in)

		require.NoError(t, err)
		assert.Equal(t, in, got)
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	t.Run("a kept secret is the saved one, never the client's ciphertext", func(t *testing.T) {
		ctx := ctxForTenant(t, testTenant)
		pkgMock.ExpectQuery(matchStoredConfigValues.String()).
			WithArgs("int-kept", testTenant).
			WillReturnRows(storedRows(
				[3]any{"url", "https://es.example:9200", false},
				[3]any{"password", mustEncrypt(t, "saved-password"), true},
			))
		// Ciphertext of some other secret — what an attacker would send to have it read.
		foreign := mustEncrypt(t, "someone-elses-secret")

		got, err := ResolveKeptSecrets(ctx, "int-kept", []IntegrationConfigValue{
			{Name: "url", Value: "https://attacker.example"},
			{Name: "password", Value: foreign, IsEncrypted: true},
		})

		require.NoError(t, err)
		assert.Equal(t, []IntegrationConfigValue{
			{Name: "url", Value: "https://attacker.example"},
			{Name: "password", Value: "saved-password"},
		}, got)
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	t.Run("legacy rows resolve to plaintext whatever their is_encrypted flag says", func(t *testing.T) {
		ctx := ctxForTenant(t, testTenant)
		// Before 2026-05-27 the upsert did not refresh is_encrypted, so a field that
		// became encrypted later can hold ciphertext under is_encrypted=false. Handing
		// that on as (ciphertext, false) would validate with ciphertext as the password,
		// and a skip_validation save would encrypt it a second time.
		pkgMock.ExpectQuery(matchStoredConfigValues.String()).
			WithArgs("int-legacy", testTenant).
			WillReturnRows(storedRows(
				[3]any{"api_key", mustEncrypt(t, "ciphertext-under-false"), false},
				[3]any{"password", "plaintext-under-false", false},
			))

		got, err := ResolveKeptSecrets(ctx, "int-legacy", []IntegrationConfigValue{
			{Name: "api_key", Value: "x", IsEncrypted: true},
			{Name: "password", Value: "x", IsEncrypted: true},
		})

		require.NoError(t, err)
		assert.Equal(t, []IntegrationConfigValue{
			{Name: "api_key", Value: "ciphertext-under-false"},
			{Name: "password", Value: "plaintext-under-false"},
		}, got)
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	t.Run("flagged without an integration id is refused", func(t *testing.T) {
		ctx := ctxForTenant(t, testTenant)

		_, err := ResolveKeptSecrets(ctx, "", []IntegrationConfigValue{{Name: "password", Value: "c", IsEncrypted: true}})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "editing a saved integration")
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	t.Run("flagged field with no saved value is refused", func(t *testing.T) {
		ctx := ctxForTenant(t, testTenant)
		// An integration id from another tenant lands here too: the tenant join
		// returns no rows.
		pkgMock.ExpectQuery(matchStoredConfigValues.String()).
			WithArgs("int-other-tenant", testTenant).
			WillReturnRows(storedRows())

		_, err := ResolveKeptSecrets(ctx, "int-other-tenant", []IntegrationConfigValue{{Name: "password", Value: "c", IsEncrypted: true}})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "password has no saved value")
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})
}

func TestRedactSecretConfigValues(t *testing.T) {
	schema := IntegrationSchema{Properties: map[string]IntegrationSchemaProperty{
		"url":      {},
		"password": {IsEncrypted: true},
		"token":    {},
	}}
	in := []IntegrationConfigValue{
		{Name: "url", Value: "https://es.example:9200"},
		{Name: "password", Value: "plaintext-password"},
		{Name: "token", Value: "webhook-token"},
		{Name: "access_secret", Value: "vm-agent-secret"},
	}

	got := redactSecretConfigValues("ES", schema, in)

	assert.Equal(t, []IntegrationConfigValue{
		{Name: "url", Value: "https://es.example:9200"},
		{Name: "password"},
		// The forms read these back: the webhook URL and the one-time vm_agent credentials.
		{Name: "token", Value: "webhook-token"},
		{Name: "access_secret", Value: "vm-agent-secret"},
	}, got)
	assert.Equal(t, "plaintext-password", in[1].Value, "the caller's slice must not be modified")

	t.Run("LLM dynamic secret keys are redacted without a schema entry", func(t *testing.T) {
		got := redactSecretConfigValues("llm", IntegrationSchema{}, []IntegrationConfigValue{
			{Name: "llm_provider", Value: "openai"},
			{Name: "llm_provider_api_key_k8s_debug", Value: "sk-agent"},
		})

		assert.Equal(t, []IntegrationConfigValue{
			{Name: "llm_provider", Value: "openai"},
			{Name: "llm_provider_api_key_k8s_debug"},
		}, got)
	})
}
