package core

// DB-gated tests for the build-request queue: the claim that serializes builds
// per tenant, the union-of-scopes coalescing, and the drain that picks up work
// enqueued mid-build. Skips cleanly when no Postgres is reachable.

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"nudgebee/services/internal/testenv"
)

const buildRequestTenant = "b2ca6e00-0000-4000-8000-000000000005"

func newBuildRequestRepo(t *testing.T) *BuildRequestRepository {
	t.Helper()
	dbm := testenv.RequireMetastore(t)
	t.Cleanup(func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_build_requests WHERE tenant_id = $1::uuid`, buildRequestTenant)
	})
	// Start from a clean slate: a previous failed run must not leave a running row
	// that makes this test look like a lock contention.
	if _, err := dbm.Exec(`DELETE FROM knowledge_graph_build_requests WHERE tenant_id = $1::uuid`, buildRequestTenant); err != nil {
		t.Fatalf("clear build requests: %v", err)
	}
	return NewBuildRequestRepository(dbm, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A burst of requests for different accounts must become ONE build covering the
// union — that is the whole point of coalescing.
func TestBuildRequestRepository_ClaimCoalescesScopes(t *testing.T) {
	repo := newBuildRequestRepo(t)

	for _, accounts := range [][]string{{"acct-a"}, {"acct-b"}, {"acct-a"}} {
		if err := repo.Enqueue(BuildRequestSpec{
			TenantID:   buildRequestTenant,
			AccountIDs: accounts,
			Trigger:    "test",
		}); err != nil {
			t.Fatalf("enqueue %v: %v", accounts, err)
		}
	}

	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil {
		t.Fatalf("TryClaim: %v", err)
	}
	if !claimed {
		t.Fatal("first TryClaim did not claim")
	}
	if !claim.Partial() {
		t.Error("claim of two account-scoped requests must be partial")
	}
	if len(claim.AccountIDs) != 2 || claim.AccountIDs[0] != "acct-a" || claim.AccountIDs[1] != "acct-b" {
		t.Errorf("claim accounts = %v, want [acct-a acct-b]", claim.AccountIDs)
	}
	// The duplicate acct-a request is deduplicated at enqueue time, so two rows.
	if len(claim.RequestIDs) != 2 {
		t.Errorf("claim covers %d requests, want 2 (the duplicate should not insert)", len(claim.RequestIDs))
	}
}

// A tenant-wide request queued alongside account-scoped ones widens the claim:
// a full rebuild already covers every narrower request.
func TestBuildRequestRepository_TenantWideRequestWidensClaim(t *testing.T) {
	repo := newBuildRequestRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-a"}, Trigger: "collector"}); err != nil {
		t.Fatalf("enqueue scoped: %v", err)
	}
	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue tenant-wide: %v", err)
	}

	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil {
		t.Fatalf("TryClaim: %v", err)
	}
	if !claimed {
		t.Fatal("TryClaim did not claim")
	}
	if claim.Partial() {
		t.Errorf("claim including a tenant-wide request must not be partial, got accounts %v", claim.AccountIDs)
	}
}

// Only one worker may build a tenant at a time — concurrent same-tenant builds
// deadlock on the node upserts.
func TestBuildRequestRepository_SecondClaimBlockedWhileRunning(t *testing.T) {
	repo := newBuildRequestRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, claimed, err := repo.TryClaim(buildRequestTenant); err != nil || !claimed {
		t.Fatalf("first TryClaim: claimed=%v err=%v", claimed, err)
	}

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-a"}, Trigger: "collector"}); err != nil {
		t.Fatalf("enqueue during build: %v", err)
	}
	_, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil {
		t.Fatalf("second TryClaim: %v", err)
	}
	if claimed {
		t.Error("second TryClaim succeeded while a build was running")
	}
}

// Work queued while a build is running must be handed back by Finish, not lost.
// This is what lets the consumer ack the losing message immediately.
func TestBuildRequestRepository_FinishDrainsWorkQueuedDuringBuild(t *testing.T) {
	repo := newBuildRequestRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil || !claimed {
		t.Fatalf("TryClaim: claimed=%v err=%v", claimed, err)
	}

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-z"}, Trigger: "collector"}); err != nil {
		t.Fatalf("enqueue during build: %v", err)
	}

	next, err := repo.Finish(claim, nil)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if next == nil {
		t.Fatal("Finish returned no follow-up claim; the queued request was lost")
	}
	if len(next.AccountIDs) != 1 || next.AccountIDs[0] != "acct-z" {
		t.Errorf("drained claim accounts = %v, want [acct-z]", next.AccountIDs)
	}

	final, err := repo.Finish(next, nil)
	if err != nil {
		t.Fatalf("Finish (drain): %v", err)
	}
	if final != nil {
		t.Errorf("Finish returned another claim when nothing was pending: %+v", final)
	}
}

// A build that failed must still resolve its rows, so a crash loop cannot wedge
// the tenant behind a permanently 'running' row.
func TestBuildRequestRepository_FinishRecordsFailure(t *testing.T) {
	repo := newBuildRequestRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil || !claimed {
		t.Fatalf("TryClaim: claimed=%v err=%v", claimed, err)
	}
	if _, err := repo.Finish(claim, errors.New("build blew up")); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// The tenant must be claimable again immediately afterwards.
	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if _, claimed, err := repo.TryClaim(buildRequestTenant); err != nil || !claimed {
		t.Fatalf("TryClaim after failure: claimed=%v err=%v", claimed, err)
	}
}

// A worker that died mid-build leaves a 'running' row behind. Once it is older
// than the staleness window the tenant must become claimable again, or one crash
// would block that tenant's builds forever.
func TestBuildRequestRepository_StaleRunningClaimIsSuperseded(t *testing.T) {
	repo := newBuildRequestRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, claimed, err := repo.TryClaim(buildRequestTenant); err != nil || !claimed {
		t.Fatalf("first TryClaim: claimed=%v err=%v", claimed, err)
	}

	// Age the running row past the window without waiting for real time.
	if _, err := repo.dbManager.Exec(`
		UPDATE knowledge_graph_build_requests
		SET started_at = NOW() - INTERVAL '2 hours'
		WHERE tenant_id = $1::uuid AND status = 'running'
	`, buildRequestTenant); err != nil {
		t.Fatalf("age running row: %v", err)
	}

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-a"}, Trigger: "collector"}); err != nil {
		t.Fatalf("enqueue after stall: %v", err)
	}
	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil {
		t.Fatalf("TryClaim after stall: %v", err)
	}
	if !claimed {
		t.Fatal("a stale running claim still blocks the tenant")
	}
	if len(claim.AccountIDs) != 1 || claim.AccountIDs[0] != "acct-a" {
		t.Errorf("claim accounts = %v, want [acct-a]", claim.AccountIDs)
	}
}

// throttledRepo returns a repository with the tenant-wide minimum interval on.
func throttledRepo(t *testing.T) *BuildRequestRepository {
	t.Helper()
	return newBuildRequestRepo(t).WithPolicy(BuildRequestPolicy{
		StaleAfter:            30 * time.Minute,
		MinTenantWideInterval: 45 * time.Minute,
	})
}

// seedFinishedTenantWideBuild inserts a terminal tenant-wide build that started
// and finished the given durations ago.
func seedFinishedTenantWideBuild(t *testing.T, repo *BuildRequestRepository, status string, startedAgo, finishedAgo time.Duration) {
	t.Helper()
	if _, err := repo.dbManager.Exec(`
		INSERT INTO knowledge_graph_build_requests
			(tenant_id, account_ids, trigger, status, requested_at, started_at, finished_at)
		VALUES ($1::uuid, '[]'::jsonb, 'cron', $2,
		        NOW() - $3::interval, NOW() - $3::interval, NOW() - $4::interval)
	`, buildRequestTenant, status, intervalString(startedAgo), intervalString(finishedAgo)); err != nil {
		t.Fatalf("seed finished build: %v", err)
	}
}

func pendingCount(t *testing.T, repo *BuildRequestRepository) int {
	t.Helper()
	var n int
	if err := repo.dbManager.Db.Get(&n, `
		SELECT COUNT(*) FROM knowledge_graph_build_requests
		WHERE tenant_id = $1::uuid AND status = 'pending'
	`, buildRequestTenant); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	return n
}

// Inside the window a tenant-wide request waits, but an account-scoped one runs
// immediately and on its own — freshness for a changed account must not be held
// hostage to the tenant-wide throttle.
func TestBuildRequestRepository_TenantWideDeferredAccountScopedStillClaimed(t *testing.T) {
	repo := throttledRepo(t)
	seedFinishedTenantWideBuild(t, repo, BuildRequestStatusDone, 11*time.Minute, 5*time.Minute)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "k8s-collector"}); err != nil {
		t.Fatalf("enqueue tenant-wide: %v", err)
	}
	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-a"}, Trigger: "cloud-collector"}); err != nil {
		t.Fatalf("enqueue account-scoped: %v", err)
	}

	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil {
		t.Fatalf("TryClaim: %v", err)
	}
	if !claimed {
		t.Fatal("account-scoped request was not claimed")
	}
	if !claim.Partial() || len(claim.AccountIDs) != 1 || claim.AccountIDs[0] != "acct-a" {
		t.Errorf("claim = partial %v accounts %v, want only acct-a (the tenant-wide request must be deferred)",
			claim.Partial(), claim.AccountIDs)
	}
	if got := pendingCount(t, repo); got != 1 {
		t.Errorf("pending rows = %d, want 1 (the deferred tenant-wide request is kept, not dropped)", got)
	}
}

// With only a tenant-wide request pending inside the window there is nothing to
// claim, and the request stays queued for the first trigger after the window.
func TestBuildRequestRepository_TenantWideOnlyNotClaimedInsideInterval(t *testing.T) {
	repo := throttledRepo(t)
	seedFinishedTenantWideBuild(t, repo, BuildRequestStatusDone, 11*time.Minute, 5*time.Minute)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "k8s-collector"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, claimed, err := repo.TryClaim(buildRequestTenant); err != nil || claimed {
		t.Fatalf("TryClaim inside the interval: claimed=%v err=%v, want not claimed", claimed, err)
	}
	if got := pendingCount(t, repo); got != 1 {
		t.Errorf("pending rows = %d, want 1", got)
	}
}

func TestBuildRequestRepository_TenantWideClaimedAfterInterval(t *testing.T) {
	repo := throttledRepo(t)
	seedFinishedTenantWideBuild(t, repo, BuildRequestStatusDone, 56*time.Minute, 50*time.Minute)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil || !claimed {
		t.Fatalf("TryClaim after the interval: claimed=%v err=%v, want claimed", claimed, err)
	}
	if claim.Partial() {
		t.Errorf("claim is partial %v, want tenant-wide", claim.AccountIDs)
	}
}

// A person pressing "rebuild now" must get a rebuild, even when a collector's
// throttled tenant-wide request is already pending. Deduplication used to fold the
// manual request into that pending row, so it silently waited out the window.
func TestBuildRequestRepository_ManualRequestBypassesInterval(t *testing.T) {
	repo := throttledRepo(t)
	seedFinishedTenantWideBuild(t, repo, BuildRequestStatusDone, 11*time.Minute, 5*time.Minute)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "k8s-collector"}); err != nil {
		t.Fatalf("enqueue collector: %v", err)
	}
	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: BuildRequestTriggerManual}); err != nil {
		t.Fatalf("enqueue manual: %v", err)
	}
	if got := pendingCount(t, repo); got != 2 {
		t.Fatalf("pending rows = %d, want 2 (a manual request must not dedupe into a collector's)", got)
	}

	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil || !claimed {
		t.Fatalf("TryClaim with a manual request pending: claimed=%v err=%v", claimed, err)
	}
	if claim.Partial() {
		t.Error("manual tenant-wide request did not produce a tenant-wide claim")
	}
	if len(claim.RequestIDs) != 2 {
		t.Errorf("claim covers %d requests, want 2 (the collector request is covered by the same build)", len(claim.RequestIDs))
	}
}

// A build that was superseded (or cleared by hand as orphaned) ran longer than
// StaleAfter and never completed. It must not start the window, or a crash would
// be followed by a second wait on top of the stale timeout.
func TestBuildRequestRepository_SupersededBuildDoesNotStartInterval(t *testing.T) {
	repo := throttledRepo(t)
	seedFinishedTenantWideBuild(t, repo, BuildRequestStatusFailed, 40*time.Minute, 5*time.Minute)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, claimed, err := repo.TryClaim(buildRequestTenant); err != nil || !claimed {
		t.Fatalf("TryClaim after a superseded build: claimed=%v err=%v, want claimed", claimed, err)
	}
}

// A build that genuinely failed does start the window, so a tenant with a broken
// source is retried once per interval instead of on every collector message.
func TestBuildRequestRepository_RealFailureStartsInterval(t *testing.T) {
	repo := throttledRepo(t)
	seedFinishedTenantWideBuild(t, repo, BuildRequestStatusFailed, 11*time.Minute, 5*time.Minute)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "k8s-collector"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, claimed, err := repo.TryClaim(buildRequestTenant); err != nil || claimed {
		t.Fatalf("TryClaim after a real failure: claimed=%v err=%v, want deferred", claimed, err)
	}
}

// Draining applies the same rule: a tenant-wide request that arrived during a
// tenant-wide build waits, while an account-scoped one drains straight away.
func TestBuildRequestRepository_FinishDrainRespectsInterval(t *testing.T) {
	repo := throttledRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil || !claimed {
		t.Fatalf("TryClaim: claimed=%v err=%v", claimed, err)
	}

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "k8s-collector"}); err != nil {
		t.Fatalf("enqueue tenant-wide during build: %v", err)
	}
	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-b"}, Trigger: "cloud-collector"}); err != nil {
		t.Fatalf("enqueue account-scoped during build: %v", err)
	}

	next, err := repo.Finish(claim, nil)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if next == nil || !next.Partial() || len(next.AccountIDs) != 1 || next.AccountIDs[0] != "acct-b" {
		t.Fatalf("drained claim = %+v, want only acct-b", next)
	}
	if got := pendingCount(t, repo); got != 1 {
		t.Errorf("pending rows = %d, want 1 (the tenant-wide request waits for the window)", got)
	}
}

// The fence. A worker whose claim was superseded — its build outlived StaleAfter
// and another worker took over — must neither overwrite 'failed' with 'done' nor
// drain the queue, which would start a second concurrent build of the tenant.
func TestBuildRequestRepository_FinishIgnoresSupersededClaim(t *testing.T) {
	repo := newBuildRequestRepo(t)

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, Trigger: "cron"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, claimed, err := repo.TryClaim(buildRequestTenant)
	if err != nil || !claimed {
		t.Fatalf("TryClaim: claimed=%v err=%v", claimed, err)
	}

	// Another worker supersedes this claim.
	if _, err := repo.dbManager.Exec(`
		UPDATE knowledge_graph_build_requests
		SET status = 'failed', finished_at = NOW(), error = 'superseded: build did not report completion'
		WHERE tenant_id = $1::uuid AND status = 'running'
	`, buildRequestTenant); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-c"}, Trigger: "cloud-collector"}); err != nil {
		t.Fatalf("enqueue for the successor: %v", err)
	}

	next, err := repo.Finish(claim, nil)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if next != nil {
		t.Errorf("superseded worker drained a claim %+v; that would be a second concurrent build", next)
	}

	var status string
	if err := repo.dbManager.Db.Get(&status, `SELECT status FROM knowledge_graph_build_requests WHERE id::text = $1`, claim.RequestIDs[0]); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != BuildRequestStatusFailed {
		t.Errorf("superseded row status = %q, want failed (not overwritten to done)", status)
	}
	if got := pendingCount(t, repo); got != 1 {
		t.Errorf("pending rows = %d, want 1 (left for the live worker)", got)
	}
}
