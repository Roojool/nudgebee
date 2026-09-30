package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMCPServer issues a new Mcp-Session-Id on every initialize, answers 400
// for requests without a session and 404 for an unknown session (as the MCP
// spec requires), and records DELETE terminations.
type fakeMCPServer struct {
	mu         sync.Mutex
	next       int
	live       map[string]bool
	deleted    []string
	deleteAuth []string
}

func (f *fakeMCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sid := r.Header.Get("Mcp-Session-Id")
	if r.Method == http.MethodDelete {
		f.deleted = append(f.deleted, sid)
		f.deleteAuth = append(f.deleteAuth, r.Header.Get("Authorization"))
		delete(f.live, sid)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var rpc struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &rpc)
	if rpc.Method == "initialize" {
		f.next++
		sid = fmt.Sprintf("s%d", f.next)
		f.live[sid] = true
		w.Header().Set("Mcp-Session-Id", sid)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{}}`))
		return
	}
	if sid == "" {
		// Per the MCP spec: a server that requires sessions answers a request
		// without one with 400, and an unknown session with 404.
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !f.live[sid] {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"session":%q}}`, sid)
}

func (f *fakeMCPServer) snapshot() (deleted, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...), append([]string(nil), f.deleteAuth...)
}

// setupFakeMCP starts a fake server, swaps in a plain HTTP client (the real one
// refuses loopback via the SSRF guard) and resets the session cache.
func setupFakeMCP(t *testing.T) (*fakeMCPServer, string) {
	t.Helper()
	fake := &fakeMCPServer{live: map[string]bool{}}
	srv := httptest.NewServer(fake)
	origClient := mcpHTTPClient
	mcpHTTPClient = srv.Client()
	resetMCPSessionCache()
	t.Cleanup(func() {
		srv.Close()
		mcpHTTPClient = origClient
		resetMCPSessionCache()
	})
	return fake, srv.URL
}

func resetMCPSessionCache() {
	mcpSessionCache.Lock()
	mcpSessionCache.sessions = map[string]mcpSessionEntry{}
	mcpSessionCache.Unlock()
}

const testMCPRawBody = `{"body":{"action_params":{"auth_type":"bearer","auth_credentials":{"bearer_token":"tok"},"method":"tools/call","params":{"name":"x"}}}}`

func callMCP(t *testing.T, cacheKey, url string) string {
	t.Helper()
	ctx := context.Background()
	raw := []byte(testMCPRawBody)
	sid, err := ensureMCPSession(ctx, cacheKey, url, raw, slog.Default())
	if err != nil {
		t.Fatalf("ensureMCPSession: %v", err)
	}
	data, status, err := doMCPRequest(ctx, cacheKey, url, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`), sid, raw, slog.Default())
	if err != nil || status != http.StatusOK {
		t.Fatalf("doMCPRequest: status=%d err=%v data=%s", status, err, data)
	}
	return data
}

func waitForDeletes(t *testing.T, fake *fakeMCPServer, n int) ([]string, []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		deleted, auth := fake.snapshot()
		if len(deleted) >= n || time.Now().After(deadline) {
			return deleted, auth
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMCPSession_SeparateCallerSessionsGetSeparateUpstreamSessions(t *testing.T) {
	_, url := setupFakeMCP(t)

	a1 := callMCP(t, url+"|acct|conv-a", url)
	b := callMCP(t, url+"|acct|conv-b", url)
	a2 := callMCP(t, url+"|acct|conv-a", url)

	if a1 == b {
		t.Fatalf("two conversations shared one upstream session: %s", a1)
	}
	if a1 != a2 {
		t.Fatalf("one conversation got two upstream sessions: %s vs %s", a1, a2)
	}
}

func TestMCPSession_UseExtendsExpiry(t *testing.T) {
	_, url := setupFakeMCP(t)
	key := url + "|acct|conv"
	callMCP(t, key, url)

	// Move expiry close to now, as if the session were created long ago but is
	// still in use; the next call must push it back out to a full TTL.
	mcpSessionCache.Lock()
	e := mcpSessionCache.sessions[key]
	e.expiry = time.Now().Add(time.Second)
	mcpSessionCache.sessions[key] = e
	mcpSessionCache.Unlock()

	callMCP(t, key, url)

	mcpSessionCache.RLock()
	remaining := time.Until(mcpSessionCache.sessions[key].expiry)
	mcpSessionCache.RUnlock()
	if remaining < mcpSessionTTL-time.Minute {
		t.Fatalf("expiry not extended on use: %s remaining", remaining)
	}
}

func TestMCPSession_ExpiredEntryIsTerminatedUpstreamAndReplaced(t *testing.T) {
	fake, url := setupFakeMCP(t)
	key := url + "|acct|conv"
	first := callMCP(t, key, url)

	mcpSessionCache.Lock()
	e := mcpSessionCache.sessions[key]
	e.expiry = time.Now().Add(-time.Second)
	mcpSessionCache.sessions[key] = e
	mcpSessionCache.Unlock()

	second := callMCP(t, key, url)
	if first == second {
		t.Fatalf("expired session was reused: %s", second)
	}
	deleted, auth := waitForDeletes(t, fake, 1)
	if len(deleted) != 1 || deleted[0] != "s1" {
		t.Fatalf("expected DELETE of s1, got %v", deleted)
	}
	if auth[0] != "Bearer tok" {
		t.Fatalf("DELETE sent without the integration's auth: %q", auth[0])
	}
}

func TestMCPSession_Upstream404ReinitializesAndRetries(t *testing.T) {
	fake, url := setupFakeMCP(t)
	key := url + "|acct|conv"
	callMCP(t, key, url)

	// Upstream drops the session (restart, its own timeout) while our cache
	// entry is still valid.
	fake.mu.Lock()
	fake.live = map[string]bool{}
	fake.mu.Unlock()

	got := callMCP(t, key, url)
	if got != `{"jsonrpc":"2.0","id":1,"result":{"session":"s2"}}` {
		t.Fatalf("expected retry on a fresh session s2, got %s", got)
	}
}

func TestIsMCPSessionGone(t *testing.T) {
	cases := []struct {
		name   string
		status int
		sid    string
		want   bool
	}{
		{"404 with session", 404, "s1", true},
		{"400 is never treated as gone", 400, "s1", false},
		{"404 without session", 404, "", false},
		{"200", 200, "s1", false},
	}
	for _, c := range cases {
		if got := isMCPSessionGone(c.status, c.sid); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMCPSession_SessionlessServerInitializedOncePerKey(t *testing.T) {
	var mu sync.Mutex
	inits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"initialize"`) {
			mu.Lock()
			inits++
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	origClient := mcpHTTPClient
	mcpHTTPClient = srv.Client()
	resetMCPSessionCache()
	t.Cleanup(func() {
		srv.Close()
		mcpHTTPClient = origClient
		resetMCPSessionCache()
	})

	for range 3 {
		callMCP(t, srv.URL+"|acct|conv", srv.URL)
	}
	mu.Lock()
	defer mu.Unlock()
	if inits != 1 {
		t.Fatalf("sessionless server should be initialized once per key, got %d", inits)
	}
}

func TestMCPSession_SessionlessEntryDroppedWhenServerRejectsCall(t *testing.T) {
	_, url := setupFakeMCP(t)
	key := url + "|acct|conv"
	mcpSessionCache.Lock()
	mcpSessionCache.sessions[key] = mcpSessionEntry{expiry: time.Now().Add(mcpSessionTTL)}
	mcpSessionCache.Unlock()

	// The fake server requires sessions: a call without one gets 400 (per the
	// MCP spec), which must clear the stale sessionless entry.
	_, status, err := doMCPRequest(context.Background(), key, url, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`), "", []byte(testMCPRawBody), slog.Default())
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("expected 400 from session-requiring server, got %d %v", status, err)
	}
	mcpSessionCache.RLock()
	_, stillCached := mcpSessionCache.sessions[key]
	mcpSessionCache.RUnlock()
	if stillCached {
		t.Fatal("stale sessionless entry should be dropped")
	}
	callMCP(t, key, url) // re-initializes and succeeds with a real session
}

func TestMCPSession_RealSessionReplacesConcurrentSessionlessEntry(t *testing.T) {
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// While our initialize is in flight, a concurrent request caches the
		// key as sessionless (a server with mixed versions mid-upgrade).
		mcpSessionCache.Lock()
		mcpSessionCache.sessions[key] = mcpSessionEntry{expiry: time.Now().Add(mcpSessionTTL)}
		mcpSessionCache.Unlock()
		w.Header().Set("Mcp-Session-Id", "real-1")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{}}`))
	}))
	origClient := mcpHTTPClient
	mcpHTTPClient = srv.Client()
	resetMCPSessionCache()
	t.Cleanup(func() {
		srv.Close()
		mcpHTTPClient = origClient
		resetMCPSessionCache()
	})
	key = srv.URL + "|acct|conv"

	sid, err := ensureMCPSession(context.Background(), key, srv.URL, []byte(testMCPRawBody), slog.Default())
	if err != nil || sid != "real-1" {
		t.Fatalf("expected the real session to win, got %q %v", sid, err)
	}
	mcpSessionCache.RLock()
	cached := mcpSessionCache.sessions[key].sessionID
	mcpSessionCache.RUnlock()
	if cached != "real-1" {
		t.Fatalf("real session should replace the sessionless entry, cached %q", cached)
	}
}
