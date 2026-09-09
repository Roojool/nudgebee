package relay

import (
	"database/sql"
	"errors"
	"log/slog"
	"nudgebee/services/internal/database"
	"sync"
	"time"
)

// k8sAgentType is the agent row the relay routes a request to when the caller
// leaves RelayExecuteRequest.AgentType empty (the relay defaults the missing
// X-NB-Agent-Type header to "k8s"). An account's other agent rows — proxy, and
// the AWS/Azure/GCP cloud-sync rows — are separate registrations that cannot
// serve an in-cluster action, so connectivity is per (account, type), never
// per account.
const k8sAgentType = "k8s"

// agentStatusConnected is the one `agent.status` value the relay treats as
// routable; every other value (NOT_CONNECTED, and any future state) is not.
const agentStatusConnected = "CONNECTED"

// Agent-connectivity cache TTLs. The relay server keeps the same answer in its
// own cache for 30 min (connected) / 2 min (not connected), so a caller can
// never observe a *fresher* answer by making the round-trip anyway. Staying
// under the relay's 2-minute negative TTL means this gate cannot delay an
// agent's reconnect beyond what the relay itself already imposes.
const (
	agentConnectedCacheTTL    = 60 * time.Second
	agentNotConnectedCacheTTL = 30 * time.Second
)

type agentStatusCacheEntry struct {
	connected bool
	expiresAt time.Time
}

var agentStatusCache = struct {
	sync.RWMutex
	entries map[string]agentStatusCacheEntry
}{entries: make(map[string]agentStatusCacheEntry)}

// AgentConnected reports whether the account's in-cluster k8s agent is currently
// registered with the relay.
//
// It mirrors the relay server's own gate exactly — the `agent` row for
// (cloud_account_id, type='k8s'), connected iff status = 'CONNECTED' — so a
// caller that skips on false is skipping a call the relay would have rejected
// with "agent not connected".
//
// Do NOT test `connection_status IS NOT NULL` instead: that column keeps the
// last heartbeat payload after the agent goes away and is never cleared, so it
// reports "connected" for clusters that were last seen in 2024.
//
// Fail-open: a database error yields true. A wasted relay round-trip is cheaper
// than mistaking a DB blip for "no agent" and silently truncating an account's
// data.
func AgentConnected(accountID string) bool {
	if accountID == "" {
		return false
	}

	agentStatusCache.RLock()
	if entry, ok := agentStatusCache.entries[accountID]; ok && time.Now().Before(entry.expiresAt) {
		agentStatusCache.RUnlock()
		return entry.connected
	}
	agentStatusCache.RUnlock()

	connected := queryAgentConnected(accountID)

	ttl := agentNotConnectedCacheTTL
	if connected {
		ttl = agentConnectedCacheTTL
	}
	agentStatusCache.Lock()
	agentStatusCache.entries[accountID] = agentStatusCacheEntry{
		connected: connected,
		expiresAt: time.Now().Add(ttl),
	}
	agentStatusCache.Unlock()

	return connected
}

func queryAgentConnected(accountID string) bool {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		slog.Error("relay: agent status lookup: no database manager", "account_id", accountID, "error", err)
		return true // fail open
	}

	var status string
	err = dbms.QueryRowAndScan(&status,
		`SELECT status FROM agent WHERE cloud_account_id = $1 AND type = $2 LIMIT 1`,
		accountID, k8sAgentType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Error("relay: agent status lookup failed", "account_id", accountID, "error", err)
	}
	return agentConnectedFromLookup(status, err)
}

// agentConnectedFromLookup is the pure policy behind AgentConnected, split out
// to be testable without a database. Only a definitive answer — a row saying
// something other than CONNECTED, or no row at all — means "no agent"; an
// undetermined answer fails open, because a wasted relay round-trip is cheaper
// than mistaking a DB blip for "no agent" and silently truncating an account's
// data.
func agentConnectedFromLookup(status string, err error) bool {
	if err == nil {
		return status == agentStatusConnected
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return true // undetermined — fail open
}
