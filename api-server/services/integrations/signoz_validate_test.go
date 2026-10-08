package integrations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/services/integrations/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSignozPassword = "secret"

// signozServer emulates a SigNoz release: version is what /api/v1/version
// reports, v1/v2 say which login API exists. A release with v2 and without v1
// serves its SPA on /api/v1/login, as v0.145 does.
func signozServer(t *testing.T, version string, v1, v2 bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": version})
		case r.URL.Path == "/api/v2/sessions/context" && v2:
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"exists":true,"orgs":[{"id":"org-1","authNSupport":{"password":[{"provider":"email_password"}]}}]}}`)
		case r.URL.Path == "/api/v2/sessions/email_password" && v2:
			if body["password"] != testSignozPassword {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"status":"error","error":{"type":"unauthenticated","code":"incorrect_password","message":"invalid email or password"}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"accessToken":"v2-token","expiresIn":1799}}`)
		case r.URL.Path == "/api/v1/login" && v1:
			if body["password"] != testSignozPassword {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"errors":[{"code":401,"msg":"invalid credentials"}]}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"accessJwt":"v1-token","accessJwtExpiry":4102444800}`)
		case r.URL.Path == "/api/v1/login":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, "<!doctype html><html></html>")
		default:
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signozCfg(url, password, mode string) []core.IntegrationConfigValue {
	return []core.IntegrationConfigValue{
		{Name: "signoz_url", Value: url},
		{Name: "signoz_username", Value: "admin@example.com"},
		{Name: "signoz_password", Value: password},
		{Name: "signoz_mode", Value: mode},
	}
}

func TestSignoz_ValidateConfig_AcceptsEverySelfHostedRelease(t *testing.T) {
	cases := []struct {
		name    string
		version string
		v1, v2  bool
	}{
		{"legacy v0.49 (v1 login)", "v0.49.1", true, false},
		{"legacy v0.51 (v1 login)", "v0.51.0", true, false},
		{"transitional v0.100 (both)", "v0.100.0", true, true},
		{"v0.132 (v2 sessions, previously rejected by the version cap)", "v0.132.4", false, true},
		{"current v0.145 (v2 sessions)", "v0.145.0", false, true},
		{"unparseable dev build", "main", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := signozServer(t, tc.version, tc.v1, tc.v2)
			errs := Signoz{}.ValidateConfig(nil, signozCfg(srv.URL, testSignozPassword, SignozModeSelfHosted), "acc-1")
			assert.Empty(t, errs)
		})
	}
}

func TestSignoz_ValidateConfig_WrongPasswordOnEveryRelease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v1, v2 bool
	}{{"legacy", true, false}, {"current", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := signozServer(t, "v0.0.0", tc.v1, tc.v2)
			errs := Signoz{}.ValidateConfig(nil, signozCfg(srv.URL, "wrong", SignozModeSelfHosted), "acc-1")
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), "invalid SigNoz credentials")
		})
	}
}

func TestSignoz_ValidateConfig_CloudModeAgainstLegacyServerFails(t *testing.T) {
	srv := signozServer(t, "v0.51.0", true, false)
	errs := Signoz{}.ValidateConfig(nil, signozCfg(srv.URL, testSignozPassword, SignozModeCloud), "acc-1")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "SigNoz Cloud")
}

func TestSignoz_ValidateConfig_NotSigNozIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<!doctype html><html></html>")
	}))
	t.Cleanup(srv.Close)

	errs := Signoz{}.ValidateConfig(nil, signozCfg(srv.URL, testSignozPassword, SignozModeSelfHosted), "acc-1")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "neither /api/v2/sessions nor /api/v1/login")
}

func TestSignoz_ValidateConfig_UnreachableHost(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	errs := Signoz{}.ValidateConfig(nil, signozCfg(url, testSignozPassword, SignozModeSelfHosted), "acc-1")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "failed to connect")
}
