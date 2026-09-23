package relay

import (
	"log/slog"
	"nudgebee/llm/common"
	"sync"
	"time"
)

// k8sAgentType is the agent row the relay routes a request to when the caller
// leaves ActionExecuteBody.AgentType empty (the relay defaults the missing
// X-NB-Agent-Type header to "k8s"). An account's other agent rows — proxy, and
// the AWS/Azure/GCP cloud-sync rows — are separate registrations that cannot
// serve an in-cluster action, so connectivity is per (account, type), never
// per account.
const k8sAgentType = "k8s"

// agentStatusConnected is the one `agent.status` value the relay treats as
// routable; every other value (NOT_CONNECTED, the empty string one prod k8s row
// carries, and any future state) is not. Compared case-insensitively for the
// same reason as `type` below: both are free-form text columns, and a row ever
// written `Connected` must not read as "no agent" and gate traffic that would
// have worked.
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

// agentStatusCache is intentionally unevicted. Its key space is closed, not
// open: keys are the cloud-account ids this process serves, and repeat lookups
// overwrite their entry rather than adding one. Size is therefore bounded by the number of accounts
// the process ever serves — ~540 on dev today, an entry being a uuid string
// plus a bool and a time.Time — so the map is tens of KB at steady state and
// does not grow with call volume. Sweeping it would add locking for no
// reclaimable memory, and would diverge from the identical cache in
// api-server's relay package that this mirrors.
var agentStatusCache = struct {
	sync.RWMutex
	entries map[string]agentStatusCacheEntry
}{entries: make(map[string]agentStatusCacheEntry)}

// AgentConnected reports whether the account's in-cluster k8s agent is currently
// registered with the relay.
//
// This mirrors api-server's relay.AgentConnected (added in cb0d9cf6b8) rather
// than importing it: llm-server is a separate Go module (nudgebee/llm) with its
// own database manager, and its relay client posts to /request directly instead
// of going through api-server's guarded relay.Execute. Without this copy, every
// tool call for an account whose agent is gone still makes the round-trip and
// takes a 503 — which is what left one dev account over the 5xx alert threshold
// 72% of the time.
//
// The check itself mirrors the relay server's own gate exactly — the `agent` row
// for (cloud_account_id, type='k8s'), connected iff status = 'CONNECTED' — so a
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
	// Only an empty account short-circuits, matching api-server's guard exactly.
	// A malformed-but-non-empty id deliberately falls through to the query and
	// fails open there: that is what api-server does, and rejecting it here
	// instead would turn every caller that passes an unexpected id into a silent
	// "agent not connected" skip rather than the visible error it is.
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

// queryGetter is the slice of *sqlx.DB the lookup needs, so the SQL can be
// exercised against sqlmock without a live metastore.
type queryGetter interface {
	Get(dest any, query string, args ...any) error
}

func queryAgentConnected(accountID string) bool {
	dbms, err := common.GetDatabaseManager(common.Metastore)
	if err != nil {
		slog.Error("relay: agent status lookup: no database manager", "account_id", accountID, "error", err)
		return true // fail open
	}
	return agentConnectedInDb(dbms.Db, accountID)
}

// agentConnectedInDb is the lookup and its policy, split out to be testable.
//
// Counting CONNECTED rows rather than reading one row's status is deliberate:
// nothing constrains an account to a single k8s agent row, and `... LIMIT 1`
// over two rows would pick one nondeterministically and could report "not
// connected" for an account that has a perfectly good agent — which, with this
// value gating the relay, would skip calls that should have worked. `lower(type)`
// and `upper(status)` for the same reason: both columns are free-form text, and
// a row written "K8s" or "Connected" must not read as absent. Neither case is
// live today (every k8s row on dev and prod is typed `k8s`, and no account has
// more than one), so this is the cheap symmetry, not a bug fix.
//
// NOTE: this is deliberately NOT byte-identical to api-server's query, which
// reads one row with `type = $2 LIMIT 1` and compares status exactly. That one
// is case-sensitive on `type` and picks nondeterministically among duplicate
// rows. The two answer the same today for every account in the metastore; see
// the PR discussion for the backport.
//
// Fail-open on error: a wasted relay round-trip is cheaper than mistaking a DB
// blip for "no agent" and silently truncating an account's data.
func agentConnectedInDb(db queryGetter, accountID string) bool {
	var connectedCount int
	if err := db.Get(&connectedCount,
		`select count(*) from agent where upper(status) = $1 and lower(type) = $2 and cloud_account_id = $3`,
		agentStatusConnected, k8sAgentType, accountID); err != nil {
		slog.Warn("relay: agent status lookup failed; assuming connected", "account_id", accountID, "error", err)
		return true // fail open
	}
	return connectedCount > 0
}
