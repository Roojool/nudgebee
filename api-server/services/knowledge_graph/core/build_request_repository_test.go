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

	claim, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute)
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

	claim, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute)
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
	if _, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute); err != nil || !claimed {
		t.Fatalf("first TryClaim: claimed=%v err=%v", claimed, err)
	}

	if err := repo.Enqueue(BuildRequestSpec{TenantID: buildRequestTenant, AccountIDs: []string{"acct-a"}, Trigger: "collector"}); err != nil {
		t.Fatalf("enqueue during build: %v", err)
	}
	_, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute)
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
	claim, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute)
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
	claim, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute)
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
	if _, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute); err != nil || !claimed {
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
	if _, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute); err != nil || !claimed {
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
	claim, claimed, err := repo.TryClaim(buildRequestTenant, 30*time.Minute)
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
