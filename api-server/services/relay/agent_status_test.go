package relay

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestAgentConnectedFromLookup pins the policy: only 'CONNECTED' counts as
// connected, a missing agent row is a definitive no, and anything undetermined
// fails open so a DB blip cannot silently strip an account of its data.
func TestAgentConnectedFromLookup(t *testing.T) {
	tests := []struct {
		name   string
		status string
		err    error
		want   bool
	}{
		{"connected", "CONNECTED", nil, true},
		{"not connected", "NOT_CONNECTED", nil, false},
		{"unknown status", "", nil, false},
		{"no agent row", "", sql.ErrNoRows, false},
		{"wrapped no rows", "", fmt.Errorf("query agent: %w", sql.ErrNoRows), false},
		{"connection refused -> fail open", "", errors.New("dial tcp: connection refused"), true},
		{"driver bad conn -> fail open", "", sql.ErrConnDone, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentConnectedFromLookup(tc.status, tc.err); got != tc.want {
				t.Errorf("agentConnectedFromLookup(%q, %v) = %v, want %v", tc.status, tc.err, got, tc.want)
			}
		})
	}
}

// TestAgentConnectedEmptyAccount asserts the guard short-circuits before
// touching the database.
func TestAgentConnectedEmptyAccount(t *testing.T) {
	if AgentConnected("") {
		t.Error(`AgentConnected("") = true, want false`)
	}
}

// TestAgentConnectedUsesCache asserts a cached answer is returned without a
// database manager — the lookup would fail open to true, so reading false back
// proves the cache was consulted.
func TestAgentConnectedUsesCache(t *testing.T) {
	const accountID = "cache-hit-account"

	agentStatusCache.Lock()
	agentStatusCache.entries[accountID] = agentStatusCacheEntry{
		connected: false,
		expiresAt: time.Now().Add(time.Minute),
	}
	agentStatusCache.Unlock()

	t.Cleanup(func() {
		agentStatusCache.Lock()
		delete(agentStatusCache.entries, accountID)
		agentStatusCache.Unlock()
	})

	if AgentConnected(accountID) {
		t.Error("AgentConnected() = true on a cached false entry, want false")
	}
}

// TestAgentNotConnectedCacheTTLUnderRelayNegativeTTL keeps this gate from
// outliving the relay server's own negative cache (2m, wsNotConnectedCacheTTL in
// relay-server/pkg/db/db_store.go). Staying under it means skipping a call here
// can never delay a reconnect longer than making the call would have.
func TestAgentNotConnectedCacheTTLUnderRelayNegativeTTL(t *testing.T) {
	const relayNotConnectedCacheTTL = 2 * time.Minute
	if agentNotConnectedCacheTTL >= relayNotConnectedCacheTTL {
		t.Errorf("agentNotConnectedCacheTTL = %s, must stay under the relay's %s negative cache",
			agentNotConnectedCacheTTL, relayNotConnectedCacheTTL)
	}
}
