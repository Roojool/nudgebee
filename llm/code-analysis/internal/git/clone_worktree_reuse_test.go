package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCloneOrReuseRepositoryAtCommit_ReusesExistingWorktreeVerbatim is the
// regression test for the "raise pr?" followup incident: a second call against
// the same worktreeDir (a followup turn in the same conversation reusing its
// persistent workspace) used to fail `git worktree add` and fall back to wiping
// the directory, silently discarding whatever the first call left there —
// including an already-applied, already-reviewed fix. It must instead reuse the
// worktree as-is when the checked-out ref still matches what was requested.
func TestCloneOrReuseRepositoryAtCommit_ReusesExistingWorktreeVerbatim(t *testing.T) {
	origin := buildOriginRepo(t)
	gc := NewGitClient(t.TempDir(), 60*time.Second, 0)
	worktree := filepath.Join(t.TempDir(), "wt")

	first, err := gc.CloneOrReuseRepositoryAtCommit(context.Background(), origin, nil, "test", "", worktree)
	if err != nil {
		t.Fatalf("first clone: %v", err)
	}

	// Simulate a fix already applied by an earlier CodeFixerAgent run in this
	// same workspace: an uncommitted edit that must survive the second call.
	markerPath := filepath.Join(worktree, "applied-fix-marker.txt")
	if err := os.WriteFile(markerPath, []byte("fix already here"), 0644); err != nil {
		t.Fatal(err)
	}

	second, err := gc.CloneOrReuseRepositoryAtCommit(context.Background(), origin, nil, "test", "", worktree)
	if err != nil {
		t.Fatalf("second clone (expected reuse): %v", err)
	}

	if second.CommitHash != first.CommitHash {
		t.Fatalf("reuse changed HEAD: first=%s second=%s", first.CommitHash, second.CommitHash)
	}
	content, readErr := os.ReadFile(markerPath)
	if readErr != nil {
		t.Fatalf("uncommitted marker was lost — worktree was wiped instead of reused: %v", readErr)
	}
	if string(content) != "fix already here" {
		t.Fatalf("marker content changed: %q", content)
	}
}

// TestCloneOrReuseRepositoryAtCommit_RecreatesWhenRefDiffers guards the safety
// side of reuse: if the caller asks for a different commit/branch than what is
// currently checked out, the existing worktree must NOT be reused — that would
// silently analyze the wrong code, which CloneSealedAtCommit's own doc comment
// calls worse than failing.
func TestCloneOrReuseRepositoryAtCommit_RecreatesWhenRefDiffers(t *testing.T) {
	origin := buildOriginRepo(t)
	gc := NewGitClient(t.TempDir(), 60*time.Second, 0)
	worktree := filepath.Join(t.TempDir(), "wt")

	baseSHA := strings.TrimSpace(gitRun(t, origin, "rev-parse", "test~1"))

	if _, err := gc.CloneOrReuseRepositoryAtCommit(context.Background(), origin, nil, "test", "", worktree); err != nil {
		t.Fatalf("first clone: %v", err)
	}

	second, err := gc.CloneOrReuseRepositoryAtCommit(context.Background(), origin, nil, "test", baseSHA, worktree)
	if err != nil {
		t.Fatalf("second clone at a different pinned commit: %v", err)
	}
	if second.CommitHash != baseSHA {
		t.Fatalf("stale worktree was reused instead of moving to the newly requested commit: got %s, want %s", second.CommitHash, baseSHA)
	}
}

// TestCloneOrReuseRepositoryAtCommit_RecoversWhenWorktreeDirDeletedExternally
// is the regression test for the "close the PR and reopen again" incident: a
// startup sweep (or anything else) can delete a persisted session workspace
// directory out from under the bare repo's worktree registration. The reuse
// check above only fires when worktreeDir still exists on disk, so with it
// gone, `worktree add` used to fail outright with "missing but already
// registered worktree" — even though there was nothing left to actually
// reuse, and a completely ordinary re-clone should have succeeded.
func TestCloneOrReuseRepositoryAtCommit_RecoversWhenWorktreeDirDeletedExternally(t *testing.T) {
	origin := buildOriginRepo(t)
	gc := NewGitClient(t.TempDir(), 60*time.Second, 0)
	worktree := filepath.Join(t.TempDir(), "wt")

	first, err := gc.CloneOrReuseRepositoryAtCommit(context.Background(), origin, nil, "test", "", worktree)
	if err != nil {
		t.Fatalf("first clone: %v", err)
	}

	// Simulate the workspace-cleanup sweep (or anything else) deleting the
	// checked-out directory while the bare repo's admin metadata still
	// references it as a registered worktree.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}

	second, err := gc.CloneOrReuseRepositoryAtCommit(context.Background(), origin, nil, "test", "", worktree)
	if err != nil {
		t.Fatalf("second clone after external deletion (expected recovery via prune): %v", err)
	}
	if second.CommitHash != first.CommitHash {
		t.Fatalf("recovered clone checked out a different commit: first=%s second=%s", first.CommitHash, second.CommitHash)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		t.Fatalf("expected a fresh worktree checkout to exist after recovery, stat failed: %v", err)
	}
}
