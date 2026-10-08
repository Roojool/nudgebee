package signozclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPassword = "correct-horse"

// fakeSignoz emulates one SigNoz release's auth surface. The response bodies
// mirror what real v0.51.0 and v0.145.0 servers returned when probed.
type fakeSignoz struct {
	v1      bool // POST /api/v1/login exists (≤ ~v0.100)
	v1HTML  bool // /api/v1/login falls through to the SPA: 200 text/html (≥ ~v0.105)
	v2      bool // /api/v2/sessions/* exist (≥ ~v0.100, and Cloud)
	spaAll  bool // every unknown route answers 200 text/html (SPA behind a proxy)
	noUser  bool // sessions/context reports exists:false
	ssoOnly bool // the org's authNSupport.password list is empty
	// loginOrgID, when set, is the only orgID email_password accepts; anything
	// else is answered with the 400 invalid-input envelope.
	loginOrgID string
	// contextStatus, when set, is returned by sessions/context instead of the
	// normal answer (e.g. 503 while SigNoz restarts).
	contextStatus int
	// noStatusField drops "status" from the sessions/context envelope.
	noStatusField bool
	// apiKey is the one SIGNOZ-API-KEY the query routes accept; keyForbidden
	// makes that key's role unable to read logs (403).
	apiKey       string
	keyForbidden bool
	expiresIn    int64

	contextHits, v1Hits, v2Hits, queryHits atomic.Int32
	queryAlways401                         bool
	reject401Next                          atomic.Bool

	mu     sync.Mutex
	seq    int
	tokens map[string]bool
}

func (f *fakeSignoz) mint() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	tok := fmt.Sprintf("tok-%d", f.seq)
	if f.tokens == nil {
		f.tokens = map[string]bool{}
	}
	f.tokens[tok] = true
	return tok
}

func (f *fakeSignoz) valid(tok string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[tok]
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func writeSPA(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><html><head></head><body></body></html>"))
}

func (f *fakeSignoz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v2/sessions/context" && f.v2 && f.contextStatus != 0:
		f.contextHits.Add(1)
		writeJSON(w, f.contextStatus, `{"status":"error","error":{"type":"unavailable","code":"unavailable","message":"service unavailable"}}`)

	case r.URL.Path == "/api/v2/sessions/context" && f.v2:
		f.contextHits.Add(1)
		password := `[{"provider":"email_password"}]`
		if f.ssoOnly {
			password = `[]`
		}
		status := `"status":"success",`
		if f.noStatusField {
			status = ""
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(
			`{%s"data":{"exists":%t,"orgs":[{"id":"org-1","name":"acme","authNSupport":{"callback":[],"password":%s}}]}}`,
			status, !f.noUser, password))

	case r.URL.Path == "/api/v2/sessions/email_password" && f.v2:
		f.v2Hits.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		wantOrg := "org-1"
		if f.loginOrgID != "" {
			wantOrg = f.loginOrgID
		}
		if body["orgID"] != wantOrg {
			writeJSON(w, http.StatusBadRequest, `{"status":"error","error":{"type":"invalid-input","code":"invalid_input","message":"orgId is not a valid uuid"}}`)
			return
		}
		if body["password"] != testPassword {
			writeJSON(w, http.StatusUnauthorized, `{"status":"error","error":{"type":"unauthenticated","code":"incorrect_password","message":"invalid email or password"}}`)
			return
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(
			`{"status":"success","data":{"tokenType":"bearer","accessToken":%q,"refreshToken":"r","expiresIn":%d}}`,
			f.mint(), f.expiresIn))

	case r.URL.Path == "/api/v1/login" && f.v1HTML:
		f.v1Hits.Add(1)
		writeSPA(w)

	case r.URL.Path == "/api/v1/login" && f.v1:
		f.v1Hits.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != testPassword {
			writeJSON(w, http.StatusUnauthorized, `{"data":null,"total":0,"limit":0,"offset":0,"errors":[{"code":401,"msg":"invalid credentials"}]}`)
			return
		}
		expiry := time.Now().Add(30 * time.Minute).Unix()
		if f.expiresIn > 0 {
			expiry = time.Now().Add(time.Duration(f.expiresIn) * time.Second).Unix()
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"userId":"u-1","accessJwt":%q,"accessJwtExpiry":%d,"refreshJwt":"r"}`, f.mint(), expiry))

	case !f.spaAll && (r.URL.Path == "/api/v4/query_range" || r.URL.Path == "/api/v3/autocomplete/attribute_keys"):
		f.queryHits.Add(1)
		if key := r.Header.Get(apiKeyHeader); key != "" {
			switch {
			case key != f.apiKey:
				writeJSON(w, http.StatusUnauthorized, `{"status":"error","errorType":"unauthorized","error":"found zero or multiple PATs with same token"}`)
			case f.keyForbidden:
				writeJSON(w, http.StatusForbidden, `{"status":"error","error":{"type":"forbidden","code":"forbidden","message":"forbidden"}}`)
			default:
				writeJSON(w, http.StatusOK, `{"status":"success","data":{"result":[],"attributeKeys":[]}}`)
			}
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if f.queryAlways401 || f.reject401Next.CompareAndSwap(true, false) || !f.valid(tok) {
			writeJSON(w, http.StatusUnauthorized, `{"status":"error","error":{"type":"unauthenticated","code":"unauthenticated","message":"unauthenticated"}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"status":"success","data":{"result":[],"attributeKeys":[]}}`)

	case f.spaAll:
		writeSPA(w)

	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 page not found\n"))
	}
}

// Server generations, as measured against real releases.
func legacy() *fakeSignoz       { return &fakeSignoz{v1: true} }               // v0.51
func transitional() *fakeSignoz { return &fakeSignoz{v1: true, v2: true} }     // ~v0.100
func modern() *fakeSignoz       { return &fakeSignoz{v2: true, v1HTML: true} } // v0.145

func serve(t *testing.T, f *fakeSignoz) (*httptest.Server, Credentials) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, Credentials{BaseURL: srv.URL, Email: "admin@example.com", Password: testPassword, Mode: "self_hosted"}
}

func TestLoginFresh_DetectsLoginAPIPerRelease(t *testing.T) {
	cases := []struct {
		name       string
		server     *fakeSignoz
		wantFlavor string
		wantV1Hits int32
	}{
		{"legacy v0.51 uses v1 login", legacy(), FlavorV1, 1},
		{"transitional release prefers v2", transitional(), FlavorV2, 0},
		{"current v0.145 uses v2 and never trusts the HTML v1 route", modern(), FlavorV2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, creds := serve(t, tc.server)
			tok, err := LoginFresh(context.Background(), http.DefaultClient, creds)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFlavor, tok.Flavor)
			assert.NotEmpty(t, tok.Value)
			assert.True(t, tok.Expiry.After(time.Now()))
			assert.Equal(t, tc.wantV1Hits, tc.server.v1Hits.Load())
		})
	}
}

func TestLoginFresh_WrongPasswordIsInvalidCredentialsOnEveryRelease(t *testing.T) {
	for name, f := range map[string]*fakeSignoz{"legacy": legacy(), "transitional": transitional(), "modern": modern()} {
		t.Run(name, func(t *testing.T) {
			_, creds := serve(t, f)
			creds.Password = "wrong"
			_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
			assert.ErrorIs(t, err, ErrInvalidCredentials)
		})
	}
}

func TestLoginFresh_CloudModeNeverFallsBackToV1(t *testing.T) {
	f := legacy()
	_, creds := serve(t, f)
	creds.Mode = ModeCloud

	_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	assert.ErrorIs(t, err, ErrNoSupportedAuth)
	assert.Contains(t, err.Error(), "SigNoz Cloud")
	assert.Zero(t, f.v1Hits.Load())
}

func TestLoginFresh_CloudModeUsesV2(t *testing.T) {
	f := modern()
	_, creds := serve(t, f)
	creds.Mode = ModeCloud

	tok, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)
	assert.Equal(t, FlavorV2, tok.Flavor)
}

func TestLoginFresh_UnknownUserOnV2DoesNotFallBackToV1(t *testing.T) {
	f := transitional()
	f.noUser = true
	_, creds := serve(t, f)

	_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	assert.ErrorIs(t, err, ErrUnknownUser)
	assert.Zero(t, f.v1Hits.Load())
	assert.Zero(t, f.v2Hits.Load())
}

func TestLoginFresh_SSOOnlyOrg(t *testing.T) {
	f := modern()
	f.ssoOnly = true
	_, creds := serve(t, f)

	_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	assert.ErrorIs(t, err, ErrPasswordAuthDisabled)
	assert.Zero(t, f.v2Hits.Load())
}

func TestLoginFresh_BadRequestCarriesSigNozMessageAndIsNotCredentials(t *testing.T) {
	f := modern()
	f.loginOrgID = "some-other-org"
	_, creds := serve(t, f)

	_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	var rejected *RequestRejectedError
	require.True(t, errors.As(err, &rejected), "got %v", err)
	assert.Equal(t, "orgId is not a valid uuid", rejected.Message)
	assert.NotErrorIs(t, err, ErrInvalidCredentials)
}

func TestLoginFresh_HTMLEverywhereIsNoSupportedAuth(t *testing.T) {
	_, creds := serve(t, &fakeSignoz{spaAll: true})

	_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	assert.ErrorIs(t, err, ErrNoSupportedAuth)
}

func TestLoginFresh_UnreachableServerIsAConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := LoginFresh(context.Background(), http.DefaultClient, Credentials{BaseURL: url, Email: "a@b.c", Password: "x"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoSupportedAuth)
	assert.Contains(t, err.Error(), "failed to connect")
}

func TestLogin_CachesTokenAcrossCalls(t *testing.T) {
	f := modern()
	_, creds := serve(t, f)

	first, err := Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)
	second, err := Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)

	assert.Equal(t, first.Value, second.Value)
	assert.Equal(t, int32(1), f.v2Hits.Load())
}

func TestLogin_ChangedPasswordMissesCache(t *testing.T) {
	f := modern()
	_, creds := serve(t, f)

	_, err := Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)

	creds.Password = "wrong"
	_, err = Login(context.Background(), http.DefaultClient, creds)
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Equal(t, int32(2), f.v2Hits.Load())
}

func TestLogin_TokenNearExpiryIsNotReused(t *testing.T) {
	f := modern()
	f.expiresIn = 30 // inside the 60s refresh skew
	_, creds := serve(t, f)

	_, err := Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)
	_, err = Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)

	assert.Equal(t, int32(2), f.v2Hits.Load())
}

func TestLoginFresh_BypassesCache(t *testing.T) {
	f := legacy()
	_, creds := serve(t, f)

	_, err := Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)
	_, err = LoginFresh(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)

	assert.Equal(t, int32(2), f.v1Hits.Load())
}

func TestDo_RelogsInAndRetriesOnceAfter401(t *testing.T) {
	for name, f := range map[string]*fakeSignoz{"legacy": legacy(), "modern": modern()} {
		t.Run(name, func(t *testing.T) {
			_, creds := serve(t, f)
			_, err := Login(context.Background(), http.DefaultClient, creds)
			require.NoError(t, err)
			f.reject401Next.Store(true) // e.g. SigNoz restarted and dropped the session

			resp, err := Do(context.Background(), http.DefaultClient, creds, http.MethodPost, "/api/v4/query_range", []byte(`{}`))
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, int32(2), f.queryHits.Load())
			assert.Equal(t, int32(2), f.v1Hits.Load()+f.v2Hits.Load(), "one login before, one after the 401")
		})
	}
}

func TestDo_DoesNotRetryASecond401(t *testing.T) {
	f := modern()
	f.queryAlways401 = true
	_, creds := serve(t, f)

	resp, err := Do(context.Background(), http.DefaultClient, creds, http.MethodPost, "/api/v4/query_range", []byte(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, int32(2), f.queryHits.Load())
}

func TestDo_LoginFailureIsReturnedNotSent(t *testing.T) {
	f := modern()
	_, creds := serve(t, f)
	creds.Password = "wrong"

	resp, err := Do(context.Background(), http.DefaultClient, creds, http.MethodPost, "/api/v4/query_range", []byte(`{}`))
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Nil(t, resp)
	assert.Zero(t, f.queryHits.Load())
}

func TestLoginFresh_UnavailableServerIsReportedNotTreatedAsLegacy(t *testing.T) {
	for _, mode := range []string{"self_hosted", ModeCloud} {
		t.Run(mode, func(t *testing.T) {
			f := transitional()
			f.contextStatus = http.StatusServiceUnavailable
			_, creds := serve(t, f)
			creds.Mode = mode

			_, err := LoginFresh(context.Background(), http.DefaultClient, creds)
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNoSupportedAuth)
			assert.Contains(t, err.Error(), "HTTP 503")
			assert.Zero(t, f.v1Hits.Load(), "a transient error must not fall back to v1 login")
		})
	}
}

func TestLoginFresh_V2DetectedWithoutStatusField(t *testing.T) {
	f := modern()
	f.noStatusField = true
	_, creds := serve(t, f)
	creds.Mode = ModeCloud

	tok, err := LoginFresh(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)
	assert.Equal(t, FlavorV2, tok.Flavor)
}

func TestWithTimeout_RestoresQueryDefault(t *testing.T) {
	assert.Equal(t, queryTimeout, withTimeout(&http.Client{}).Timeout)

	own := &http.Client{Timeout: 5 * time.Second}
	assert.Same(t, own, withTimeout(own), "a client with its own timeout is used as-is")
}

func TestLogin_PrunesExpiredEntries(t *testing.T) {
	tokenMu.Lock()
	tokenCache["stale|entry"] = Token{Value: "old", Expiry: time.Now().Add(-time.Hour)}
	tokenMu.Unlock()

	_, creds := serve(t, modern())
	_, err := Login(context.Background(), http.DefaultClient, creds)
	require.NoError(t, err)

	tokenMu.Lock()
	defer tokenMu.Unlock()
	assert.NotContains(t, tokenCache, "stale|entry")
	assert.Contains(t, tokenCache, cacheKey(creds))
}

const testAPIKey = "key-1"

func keyCreds(srvURL string) Credentials {
	// Stale password-type values are present on purpose: a row switched to API
	// keys keeps them, and they must not be used.
	return Credentials{BaseURL: srvURL, AuthType: AuthAPIKey, APIKey: testAPIKey, Email: "admin@example.com", Password: testPassword}
}

func TestDo_APIKeySendsKeyAndNeverLogsIn(t *testing.T) {
	for name, f := range map[string]*fakeSignoz{"legacy": legacy(), "modern": modern()} {
		t.Run(name, func(t *testing.T) {
			f.apiKey = testAPIKey
			srv, _ := serve(t, f)

			resp, err := Do(context.Background(), http.DefaultClient, keyCreds(srv.URL), http.MethodPost, "/api/v4/query_range", []byte(`{}`))
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Zero(t, f.contextHits.Load()+f.v1Hits.Load()+f.v2Hits.Load(), "an API key must not trigger a login")
		})
	}
}

func TestDo_APIKey401IsNotRetried(t *testing.T) {
	f := modern()
	f.apiKey = "a-different-key"
	srv, _ := serve(t, f)

	resp, err := Do(context.Background(), http.DefaultClient, keyCreds(srv.URL), http.MethodPost, "/api/v4/query_range", []byte(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, int32(1), f.queryHits.Load())
	assert.Zero(t, f.v1Hits.Load()+f.v2Hits.Load(), "a rejected key must not fall back to the stale password")
}

func TestDo_PasswordAuthIgnoresStaleAPIKey(t *testing.T) {
	f := modern()
	f.apiKey = testAPIKey
	_, creds := serve(t, f)
	creds.AuthType = AuthPassword
	creds.APIKey = testAPIKey // left over from before the row switched back to password

	resp, err := Do(context.Background(), http.DefaultClient, creds, http.MethodPost, "/api/v4/query_range", []byte(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(1), f.v2Hits.Load(), "password auth logs in rather than sending the stale key")
}

func TestCheckAPIKey(t *testing.T) {
	cases := []struct {
		name    string
		server  *fakeSignoz
		key     string
		wantErr error
	}{
		{"valid key on legacy", &fakeSignoz{v1: true, apiKey: testAPIKey}, testAPIKey, nil},
		{"valid key on current", &fakeSignoz{v2: true, v1HTML: true, apiKey: testAPIKey}, testAPIKey, nil},
		{"wrong key", &fakeSignoz{v2: true, apiKey: testAPIKey}, "nope", ErrInvalidAPIKey},
		{"key whose role may not read logs", &fakeSignoz{v2: true, apiKey: testAPIKey, keyForbidden: true}, testAPIKey, ErrAPIKeyForbidden},
		{"URL serves a SPA, not SigNoz", &fakeSignoz{spaAll: true}, testAPIKey, ErrNotSigNozAPI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(t, tc.server)
			creds := keyCreds(srv.URL)
			creds.APIKey = tc.key

			err := CheckAPIKey(context.Background(), http.DefaultClient, creds)
			if tc.wantErr == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestCheckAPIKey_NotFoundIsNotSigNoz(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	err := CheckAPIKey(context.Background(), http.DefaultClient, keyCreds(srv.URL))
	assert.ErrorIs(t, err, ErrNotSigNozAPI)
}

func TestLogin_RefusesAPIKeyCredentials(t *testing.T) {
	f := modern()
	srv, _ := serve(t, f)

	_, err := Login(context.Background(), http.DefaultClient, keyCreds(srv.URL))
	assert.ErrorIs(t, err, errAPIKeyLogin)
	_, err = LoginFresh(context.Background(), http.DefaultClient, keyCreds(srv.URL))
	assert.ErrorIs(t, err, errAPIKeyLogin)
	assert.Zero(t, f.contextHits.Load()+f.v1Hits.Load()+f.v2Hits.Load())
}
