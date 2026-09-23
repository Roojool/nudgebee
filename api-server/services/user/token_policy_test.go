package user

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeKind(t *testing.T) {
	// Omitted kind mints an all key: older clients of users_create_token send no kind,
	// and a PAT has always opened both doors. Narrowing is the feature flag's job.
	k, err := normalizeKind("")
	assert.Nil(t, err)
	assert.Equal(t, "all", k)

	k, err = normalizeKind("gateway")
	assert.Nil(t, err)
	assert.Equal(t, "gateway", k)

	k, err = normalizeKind("platform")
	assert.Nil(t, err)
	assert.Equal(t, "platform", k)

	k, err = normalizeKind("all")
	assert.Nil(t, err)
	assert.Equal(t, "all", k)

	_, err = normalizeKind("admin")
	assert.NotNil(t, err)
}

func TestCarriesGatewayTraffic(t *testing.T) {
	assert.True(t, carriesGatewayTraffic(KindGateway))
	assert.True(t, carriesGatewayTraffic(KindAll))
	assert.False(t, carriesGatewayTraffic(KindPlatform))
	assert.False(t, carriesGatewayTraffic(""))
}

func TestValidateLimitsForKind(t *testing.T) {
	limits := []TokenLimit{{Metric: "cost", Period: "month", Value: 20}}

	// A platform key carries expiry and nothing else (D8).
	err := validateLimitsForKind("platform", limits)
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "platform")

	// A kind that never reached normalizeKind rejects limits rather than storing them.
	err = validateLimitsForKind("something-else", limits)
	assert.NotNil(t, err)

	assert.Nil(t, validateLimitsForKind("platform", nil))
	assert.Nil(t, validateLimitsForKind("gateway", limits))

	// An all-access key reaches the gateway, so its quotas are meaningful and accepted.
	assert.Nil(t, validateLimitsForKind("all", limits))
	assert.NotNil(t, validateLimitsForKind("all", []TokenLimit{{Metric: "gpus", Period: "month", Value: 1}}))

	// Unknown metric or period is rejected rather than stored and ignored.
	assert.NotNil(t, validateLimitsForKind("gateway", []TokenLimit{{Metric: "gpus", Period: "month", Value: 1}}))
	assert.NotNil(t, validateLimitsForKind("gateway", []TokenLimit{{Metric: "cost", Period: "fortnight", Value: 1}}))
	assert.NotNil(t, validateLimitsForKind("gateway", []TokenLimit{{Metric: "cost", Period: "month", Value: 0}}))

	// Same (metric, period, model) twice would double-count one counter; a different model is fine.
	err = validateLimitsForKind("gateway", []TokenLimit{
		{Metric: "requests", Period: "minute", Value: 5},
		{Metric: "requests", Period: "minute", Value: 10},
	})
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Nil(t, validateLimitsForKind("gateway", []TokenLimit{
		{Metric: "requests", Period: "minute", Value: 5},
		{Metric: "requests", Period: "minute", Value: 5, Model: "gpt-5"},
	}))
}

func TestDeleteRequestNeedsAnIdentifier(t *testing.T) {
	assert.NotNil(t, validateDeleteRequest(UserTokenDeleteRequest{}))
	assert.Nil(t, validateDeleteRequest(UserTokenDeleteRequest{Id: "key-uuid-1"}))
	assert.Nil(t, validateDeleteRequest(UserTokenDeleteRequest{Name: "laptop"}))
}

func TestResolveDeleteID(t *testing.T) {
	// No match: not found.
	_, err := resolveDeleteID(nil)
	assert.NotNil(t, err)

	// Exactly one match: that id.
	id, err := resolveDeleteID([]string{"key-uuid-1"})
	assert.Nil(t, err)
	assert.Equal(t, "key-uuid-1", id)

	// A name shared across kinds is ambiguous — must not guess which one was meant.
	_, err = resolveDeleteID([]string{"key-uuid-1", "key-uuid-2"})
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

func TestValidateLimitsAgainstCeilings(t *testing.T) {
	ceilings := []TokenLimit{
		{Metric: "cost", Period: "month", Value: 100},
	}

	// Below the ceiling passes.
	assert.Nil(t, validateLimitsAgainstCeilings(ceilings, []TokenLimit{{Metric: "cost", Period: "month", Value: 50}}))

	// Exactly at the ceiling passes.
	assert.Nil(t, validateLimitsAgainstCeilings(ceilings, []TokenLimit{{Metric: "cost", Period: "month", Value: 100}}))

	// Above the ceiling is rejected.
	err := validateLimitsAgainstCeilings(ceilings, []TokenLimit{{Metric: "cost", Period: "month", Value: 150}})
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "ceiling")

	// A metric+period with no configured ceiling passes through untouched.
	assert.Nil(t, validateLimitsAgainstCeilings(ceilings, []TokenLimit{{Metric: "requests", Period: "minute", Value: 1000000}}))
}

// TestLongestTokenFitsBcryptLimit guards the bcrypt 72-byte input limit: HashPassword
// hashes the whole prefixed token, so prefix + hex-encoded random must never exceed 72
// bytes for any prefix in use. A future longer prefix (or a bumped TokenRandomBytes)
// should fail this test rather than fail bcrypt.GenerateFromPassword in production.
func TestLongestTokenFitsBcryptLimit(t *testing.T) {
	const bcryptMaxBytes = 72
	randomChars := TokenRandomBytes * 2 // hex-encoded

	for _, prefix := range []string{TokenPrefix, GatewayTokenPrefix} {
		length := len(prefix) + randomChars
		assert.LessOrEqualf(t, length, bcryptMaxBytes,
			"token with prefix %q is %d bytes, exceeds bcrypt's %d-byte limit", prefix, length, bcryptMaxBytes)
	}
}

func TestTightestCeiling(t *testing.T) {
	ceilings := []TokenLimit{
		{Metric: "cost", Period: "month", Value: 500},
		{Metric: "cost", Period: "month", Value: 50},
		{Metric: "tokens", Period: "day", Value: 1000},
	}
	v, ok := tightestCeiling(ceilings, "cost", "month")
	assert.True(t, ok)
	assert.Equal(t, 50.0, v)

	_, ok = tightestCeiling(ceilings, "requests", "minute")
	assert.False(t, ok)
}
