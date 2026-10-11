package user

import (
	"encoding/json"
	"log/slog"
	"nudgebee/services/internal/testenv"
	"nudgebee/services/security"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateUserAuthToken(t *testing.T) {
	testenv.RequireEnv(t, testenv.User, testenv.Tenant)
	sc := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), slog.Default(), nil, nil)
	request, err := CreateUserAuthToken(sc, UserTokenCreateRequest{
		Name: "test",
	})
	assert.Nil(t, err)
	assert.NotNil(t, request)
	assert.NotEmpty(t, request.Token)
	assert.NotEmpty(t, request.Name)
}

func TestUserAuthTokenJSONSerialization(t *testing.T) {
	// Null AccessedAt serializes to "accessed_at": null
	token := UserAuthToken{
		Id:         "tok-1",
		Name:       "my-token",
		Provider:   "token",
		Status:     "active",
		CreatedAt:  "2026-09-10T17:49:00Z",
		AccessedAt: nil,
	}
	bytes, err := json.Marshal(token)
	assert.Nil(t, err)
	assert.Contains(t, string(bytes), `"accessed_at":null`)

	// Non-null AccessedAt serializes to string timestamp
	ts := "2026-09-11T10:00:00Z"
	token.AccessedAt = &ts
	bytes, err = json.Marshal(token)
	assert.Nil(t, err)
	assert.Contains(t, string(bytes), `"accessed_at":"2026-09-11T10:00:00Z"`)
}
