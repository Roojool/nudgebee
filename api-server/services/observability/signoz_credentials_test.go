package observability

import (
	"testing"

	"nudgebee/services/integrations/core"
	"nudgebee/services/integrations/signozclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func signozRow(values map[string]string) []core.IntegrationConfigValue {
	var configs []core.IntegrationConfigValue
	for name, value := range values {
		configs = append(configs, core.IntegrationConfigValue{Name: name, Value: value})
	}
	return configs
}

func TestSignozCredentials_LegacyRowIsPassword(t *testing.T) {
	creds, err := signozCredentials(signozRow(map[string]string{
		SignozUrl: "http://signoz:3301", SignozUserName: "admin@example.com", SignozPassword: "pw", SignozMode: "self_hosted",
	}))
	require.NoError(t, err)
	assert.Equal(t, signozclient.AuthPassword, creds.AuthType)
	assert.Equal(t, "admin@example.com", creds.Email)
	assert.Equal(t, "pw", creds.Password)
}

// A row switched between auth types keeps the other type's stored values; only
// the selected type's values may reach the credentials.
func TestSignozCredentials_IgnoresTheOtherTypesStaleValues(t *testing.T) {
	keyRow, err := signozCredentials(signozRow(map[string]string{
		SignozUrl: "http://signoz:8080", SignozAuthType: signozclient.AuthAPIKey, SignozAPIKey: "key-1",
		SignozUserName: "old@example.com", SignozPassword: "old-pw",
	}))
	require.NoError(t, err)
	assert.Equal(t, "key-1", keyRow.APIKey)
	assert.Empty(t, keyRow.Email)
	assert.Empty(t, keyRow.Password)

	pwRow, err := signozCredentials(signozRow(map[string]string{
		SignozUrl: "http://signoz:8080", SignozAuthType: signozclient.AuthPassword, SignozUserName: "admin@example.com",
		SignozPassword: "pw", SignozAPIKey: "revoked-key",
	}))
	require.NoError(t, err)
	assert.Empty(t, pwRow.APIKey)
	assert.Equal(t, "pw", pwRow.Password)
}

func TestSignozCredentials_MissingValuesForTheSelectedType(t *testing.T) {
	_, err := signozCredentials(signozRow(map[string]string{
		SignozUrl: "http://signoz:8080", SignozAuthType: signozclient.AuthAPIKey,
		SignozUserName: "admin@example.com", SignozPassword: "pw",
	}))
	assert.Error(t, err, "an API-key row must not fall back to stored password values")

	_, err = signozCredentials(signozRow(map[string]string{
		SignozUrl: "http://signoz:8080", SignozAuthType: "oauth",
	}))
	assert.ErrorContains(t, err, "unknown signoz auth type")
}

// Only the selected type's secret is decrypted: a leftover secret of the other
// type that can't be decrypted must not break a working row.
func TestSignozCredentials_UndecryptableStaleSecretIsNotRead(t *testing.T) {
	garbage := core.IntegrationConfigValue{IsEncrypted: true, Value: "not-hex-ciphertext"}

	keyRow := signozRow(map[string]string{SignozUrl: "http://signoz:8080", SignozAuthType: signozclient.AuthAPIKey, SignozAPIKey: "key-1"})
	stalePassword := garbage
	stalePassword.Name = SignozPassword
	creds, err := signozCredentials(append(keyRow, stalePassword))
	require.NoError(t, err)
	assert.Equal(t, "key-1", creds.APIKey)

	pwRow := signozRow(map[string]string{SignozUrl: "http://signoz:8080", SignozUserName: "admin@example.com", SignozPassword: "pw"})
	staleKey := garbage
	staleKey.Name = SignozAPIKey
	creds, err = signozCredentials(append(pwRow, staleKey))
	require.NoError(t, err)
	assert.Equal(t, "pw", creds.Password)
}
