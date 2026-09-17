package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForCondition polls fn until it returns true or the timeout elapses,
// failing the test if the condition never became true.
func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// TestBackgroundExecTool_DescriptionDisclosesNoTerminal guards against this
// tool's own docs silently drifting into implying interactive/PTY behavior
// it doesn't have — reviewed for exactly this ambiguity (#38441).
func TestBackgroundExecTool_DescriptionDisclosesNoTerminal(t *testing.T) {
	desc := NewBackgroundExecTool(t.TempDir()).Description()
	if !strings.Contains(desc, "No terminal is attached") {
		t.Fatalf("expected the description to disclose there is no PTY/stdin, got: %q", desc)
	}
}

func TestBackgroundExecTool_StartReadKill_HappyPath(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	startResp := tool.Execute(ctx, map[string]any{"action": "start", "command": "echo hello-background"})
	if startResp.Status != "success" {
		t.Fatalf("start failed: %+v", startResp)
	}
	data, ok := startResp.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T: %+v", startResp.Data, startResp.Data)
	}
	sessionID, _ := data["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("expected non-empty session_id, got: %+v", data)
	}

	var out backgroundExecOutput
	waitForCondition(t, 3*time.Second, func() bool {
		readResp := tool.Execute(ctx, map[string]any{"action": "read", "session_id": sessionID})
		if readResp.Status != "success" {
			t.Fatalf("read failed: %+v", readResp)
		}
		out = readResp.Data.(backgroundExecOutput)
		return out.Status == "exited"
	})

	if out.ExitCode == nil || *out.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %+v", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "hello-background") {
		t.Fatalf("expected stdout to contain echoed text, got: %q", out.Stdout)
	}

	// Killing an already-exited session is a no-op, not an error.
	killResp := tool.Execute(ctx, map[string]any{"action": "kill", "session_id": sessionID})
	if killResp.Status != "success" {
		t.Fatalf("kill of exited session should succeed, got: %+v", killResp)
	}
	killData := killResp.Data.(map[string]any)
	if already, _ := killData["already_exited"].(bool); !already {
		t.Fatalf("expected already_exited=true, got: %+v", killData)
	}
}

func TestBackgroundExecTool_KillStopsARunningProcess(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	startResp := tool.Execute(ctx, map[string]any{"action": "start", "command": "sleep 30"})
	if startResp.Status != "success" {
		t.Fatalf("start failed: %+v", startResp)
	}
	sessionID := startResp.Data.(map[string]any)["session_id"].(string)

	// Give the process a moment to actually start before killing it.
	waitForCondition(t, 2*time.Second, func() bool {
		readResp := tool.Execute(ctx, map[string]any{"action": "read", "session_id": sessionID})
		return readResp.Data.(backgroundExecOutput).Status == "running"
	})

	killResp := tool.Execute(ctx, map[string]any{"action": "kill", "session_id": sessionID})
	if killResp.Status != "success" {
		t.Fatalf("kill failed: %+v", killResp)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		readResp := tool.Execute(ctx, map[string]any{"action": "read", "session_id": sessionID})
		return readResp.Data.(backgroundExecOutput).Status == "exited"
	})
}

func TestBackgroundExecTool_ContextCancellationKillsOutstandingSession(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx, cancel := context.WithCancel(context.Background())

	startResp := tool.Execute(ctx, map[string]any{"action": "start", "command": "sleep 30"})
	if startResp.Status != "success" {
		t.Fatalf("start failed: %+v", startResp)
	}
	sessionID := startResp.Data.(map[string]any)["session_id"].(string)

	waitForCondition(t, 2*time.Second, func() bool {
		readResp := tool.Execute(context.Background(), map[string]any{"action": "read", "session_id": sessionID})
		return readResp.Data.(backgroundExecOutput).Status == "running"
	})

	// Simulates the analysis run ending: ReActPlanner.Plan's ctx is canceled
	// (success, failure, or MaxProcessingTime) without the agent ever calling
	// kill. exec.CommandContext must tear the process down on its own.
	cancel()

	waitForCondition(t, 2*time.Second, func() bool {
		readResp := tool.Execute(context.Background(), map[string]any{"action": "read", "session_id": sessionID})
		return readResp.Data.(backgroundExecOutput).Status == "exited"
	})
}

func TestBackgroundExecTool_BlocksDangerousCommand(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	resp := tool.Execute(ctx, map[string]any{"action": "start", "command": "sudo rm -rf /"})
	if resp.Status != "error" || !strings.Contains(resp.Error, "Command blocked for security reasons") {
		t.Fatalf("expected the same blocklist cli_tool enforces to reject this, got: %+v", resp)
	}
}

func TestBackgroundExecTool_RestrictedPROperationIsRedirectedNotStarted(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	tool.SetRestrictPROperations(true)
	ctx := context.Background()

	resp := tool.Execute(ctx, map[string]any{"action": "start", "command": "gh pr create --title x --body y"})
	if resp.Status != "success" {
		t.Fatalf("expected a success response carrying the redirect (mirrors cli_tool), got: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if blocked, _ := data["blocked"].(bool); !blocked {
		t.Fatalf("expected blocked=true, got: %+v", data)
	}
}

func TestBackgroundExecTool_UnknownSessionID(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	for _, action := range []string{"read", "kill"} {
		resp := tool.Execute(ctx, map[string]any{"action": action, "session_id": "bg-does-not-exist"})
		if resp.Status != "error" {
			t.Fatalf("%s on unknown session_id should error, got: %+v", action, resp)
		}
	}
}

func TestBackgroundExecTool_UnknownAction(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	resp := tool.Execute(context.Background(), map[string]any{"action": "frobnicate"})
	if resp.Status != "error" {
		t.Fatalf("expected an error for an unknown action, got: %+v", resp)
	}
}

func TestBackgroundExecTool_ConcurrencyCapBlocksAFifthSession(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	var sessionIDs []string
	for i := 0; i < maxConcurrentBackgroundSessions; i++ {
		resp := tool.Execute(ctx, map[string]any{"action": "start", "command": "sleep 30"})
		if resp.Status != "success" {
			t.Fatalf("start #%d failed: %+v", i, resp)
		}
		sessionIDs = append(sessionIDs, resp.Data.(map[string]any)["session_id"].(string))
	}

	overCap := tool.Execute(ctx, map[string]any{"action": "start", "command": "sleep 30"})
	if overCap.Status != "error" {
		t.Fatalf("expected the %dth concurrent session to be rejected, got: %+v", maxConcurrentBackgroundSessions+1, overCap)
	}

	// Freeing one slot lets a new session start.
	killResp := tool.Execute(ctx, map[string]any{"action": "kill", "session_id": sessionIDs[0]})
	if killResp.Status != "success" {
		t.Fatalf("kill failed: %+v", killResp)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		readResp := tool.Execute(ctx, map[string]any{"action": "read", "session_id": sessionIDs[0]})
		return readResp.Data.(backgroundExecOutput).Status == "exited"
	})

	afterFree := tool.Execute(ctx, map[string]any{"action": "start", "command": "sleep 30"})
	if afterFree.Status != "success" {
		t.Fatalf("expected a session to start once a slot freed up, got: %+v", afterFree)
	}
}

func TestBackgroundExecTool_OutputCapDropsFromFront(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	// Print well over backgroundOutputCap bytes so the buffer must drop from
	// the front, then confirm the tail (not the head) survives.
	cmd := fmt.Sprintf("i=0; while [ $i -lt %d ]; do echo \"line-$i-marker-END\"; i=$((i+1)); done", 3000)
	startResp := tool.Execute(ctx, map[string]any{"action": "start", "command": cmd})
	if startResp.Status != "success" {
		t.Fatalf("start failed: %+v", startResp)
	}
	sessionID := startResp.Data.(map[string]any)["session_id"].(string)

	var out backgroundExecOutput
	waitForCondition(t, 5*time.Second, func() bool {
		readResp := tool.Execute(ctx, map[string]any{"action": "read", "session_id": sessionID})
		out = readResp.Data.(backgroundExecOutput)
		return out.Status == "exited"
	})

	if out.StdoutDroppedBytes == 0 {
		t.Fatalf("expected some stdout bytes to have been dropped for a %d-line loop, got 0 dropped (buffer cap=%d)", 3000, backgroundOutputCap)
	}
	if len(out.Stdout) > backgroundOutputCap {
		t.Fatalf("buffered stdout (%d bytes) exceeds the cap (%d)", len(out.Stdout), backgroundOutputCap)
	}
	if !strings.Contains(out.Stdout, "line-2999-marker-END") {
		t.Fatalf("expected the tail (most recent output) to survive, got: %q", out.Stdout[:min(200, len(out.Stdout))])
	}
	if strings.Contains(out.Stdout, "line-0-marker-END") {
		t.Fatalf("expected the head (oldest output) to have been dropped, but it's still present")
	}
}

// TestBackgroundExecTool_KillTerminatesForkedChildNotJustParent reproduces
// the shape of a real dev server (a supervisor process that forks a worker
// and waits on it, like `npm run dev` forking node) — the class of process
// flagged in review as at risk of being orphaned by killing only cmd.Process.
func TestBackgroundExecTool_KillTerminatesForkedChildNotJustParent(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	childPIDFile := filepath.Join(workspace, "child.pid")
	cmd := fmt.Sprintf("sleep 30 & echo $! > %s; wait", childPIDFile)
	startResp := tool.Execute(ctx, map[string]any{"action": "start", "command": cmd})
	if startResp.Status != "success" {
		t.Fatalf("start failed: %+v", startResp)
	}
	sessionID := startResp.Data.(map[string]any)["session_id"].(string)

	var childPID int
	waitForCondition(t, 2*time.Second, func() bool {
		data, err := os.ReadFile(childPIDFile)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return false
		}
		childPID = pid
		return true
	})

	killResp := tool.Execute(ctx, map[string]any{"action": "kill", "session_id": sessionID})
	if killResp.Status != "success" {
		t.Fatalf("kill failed: %+v", killResp)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		// Signal 0 checks whether the process exists without actually
		// signaling it; ESRCH means it's gone.
		return syscall.Kill(childPID, 0) != nil
	})
}

func TestBackgroundExecTool_EvictsOldestExitedSessionsOverCap(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBackgroundExecTool(workspace)
	ctx := context.Background()

	var ids []string
	for i := 0; i < maxTrackedSessions+3; i++ {
		resp := tool.Execute(ctx, map[string]any{"action": "start", "command": "true"})
		if resp.Status != "success" {
			t.Fatalf("start #%d failed: %+v", i, resp)
		}
		id := resp.Data.(map[string]any)["session_id"].(string)
		waitForCondition(t, 2*time.Second, func() bool {
			r := tool.Execute(ctx, map[string]any{"action": "read", "session_id": id})
			return r.Status == "success" && r.Data.(backgroundExecOutput).Status == "exited"
		})
		ids = append(ids, id)
	}

	tool.mu.Lock()
	trackedCount := len(tool.sessions)
	tool.mu.Unlock()
	if trackedCount > maxTrackedSessions {
		t.Fatalf("expected at most %d tracked sessions, got %d", maxTrackedSessions, trackedCount)
	}

	if oldest := tool.Execute(ctx, map[string]any{"action": "read", "session_id": ids[0]}); oldest.Status != "error" {
		t.Fatalf("expected the oldest exited session to have been evicted, got: %+v", oldest)
	}
	if newest := tool.Execute(ctx, map[string]any{"action": "read", "session_id": ids[len(ids)-1]}); newest.Status != "success" {
		t.Fatalf("expected the most recently exited session to still be readable, got: %+v", newest)
	}
}

// TestBackgroundExecTool_BlocksForeignRepoPRLookup guards against a regression
// where cli_background could reach the GitHub API without the repoScopeGuard
// applied to cli/gh — a background command asking about a PR/issue in a
// repository other than the one checked out must be refused the same way.
func TestBackgroundExecTool_BlocksForeignRepoPRLookup(t *testing.T) {
	repoDir := newRepoDir(t, "https://github.com/nudgebee/nudgebee-enterprise.git")
	tool := NewBackgroundExecTool(repoDir)
	ctx := context.Background()

	resp := tool.Execute(ctx, map[string]any{
		"action":  "start",
		"command": "curl -s https://api.github.com/repos/metabase/metabase/pulls/35094",
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "blocked") {
		t.Fatalf("expected the foreign-repo PR lookup to be refused like cli/gh refuse it, got: %+v", resp)
	}
}
