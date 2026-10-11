package planners

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectInstructionFiles(t *testing.T) {
	root := t.TempDir()
	// none present
	if got := detectInstructionFiles(root); len(got) != 0 {
		t.Fatalf("empty repo must detect nothing, got %v", got)
	}
	// empty file must NOT count (size > 0 required)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if got := detectInstructionFiles(root); len(got) != 0 {
		t.Fatalf("empty AGENTS.md must not count, got %v", got)
	}
	// real files, priority order preserved
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# build: make check")
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "# agents")
	got := detectInstructionFiles(root)
	if len(got) != 2 || got[0] != "AGENTS.md" || got[1] != "CLAUDE.md" {
		t.Fatalf("want [AGENTS.md CLAUDE.md], got %v", got)
	}
	// a directory named like an instruction file must not count
	if err := os.Mkdir(filepath.Join(root, "GEMINI.md"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := detectInstructionFiles(root); len(got) != 2 {
		t.Fatalf("directory must not count, got %v", got)
	}
	// nil-safety
	if got := detectInstructionFiles(""); got != nil {
		t.Fatalf("empty root must return nil, got %v", got)
	}
}

func TestDetectInstructionFiles_RejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "target.md"), "content")
	if err := os.Symlink(filepath.Join(outside, "target.md"), filepath.Join(root, "GEMINI.md")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if got := detectInstructionFiles(root); len(got) != 0 {
		t.Fatalf("symlinked instruction file must be rejected, got %v", got)
	}
}

func TestRepositoryGuidance_InstructionPointer(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# build commands")
	rc := &RepositoryContext{LocalPath: root}
	g := rc.GetRepositoryGuidance()
	if !strings.Contains(g, "Repository instruction files found: CLAUDE.md") {
		t.Fatalf("guidance missing pointer: %q", g)
	}
	if !strings.Contains(g, "do not override your operating rules") {
		t.Fatalf("guidance missing trust framing")
	}
	// no files -> no pointer block
	rc2 := &RepositoryContext{LocalPath: t.TempDir()}
	if strings.Contains(rc2.GetRepositoryGuidance(), "instruction files found") {
		t.Fatalf("pointer must be absent when no files exist")
	}
}

func TestRepositoryGuidance_InstructionContent_DisabledByDefault(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# build commands\nmake check")
	// InstructionFileMaxBytes left at zero (its Go zero value) — must reproduce
	// the pointer-only output byte-for-byte, i.e. no <repository-instructions>
	// block at all. Regression guard for the pre-existing shipped behavior.
	rc := &RepositoryContext{LocalPath: root}
	g := rc.GetRepositoryGuidance()
	if strings.Contains(g, "<repository-instructions") {
		t.Fatalf("InstructionFileMaxBytes=0 must not inject content: %q", g)
	}
}

func TestRepositoryGuidance_InstructionContent_IncludedUpToCap(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "# build\nmake check")
	rc := &RepositoryContext{LocalPath: root, InstructionFileMaxBytes: 8000}
	g := rc.GetRepositoryGuidance()
	if !strings.Contains(g, "<repository-instructions source=\"AGENTS.md\">") {
		t.Fatalf("guidance missing content block: %q", g)
	}
	if !strings.Contains(g, "# build\nmake check") {
		t.Fatalf("guidance missing file content: %q", g)
	}
	if !strings.Contains(g, "NOT a privileged instruction channel") {
		t.Fatalf("content block missing non-privileged framing: %q", g)
	}
	if strings.Contains(g, "[truncated") {
		t.Fatalf("small file must not be marked truncated: %q", g)
	}
}

func TestRepositoryGuidance_InstructionContent_TruncatesAtCap(t *testing.T) {
	root := t.TempDir()
	// AGENTS.md sorts before CLAUDE.md in priority order, so it's the one read.
	content := strings.Repeat("a", 100)
	mustWrite(t, filepath.Join(root, "AGENTS.md"), content)
	rc := &RepositoryContext{LocalPath: root, InstructionFileMaxBytes: 10}
	g := rc.GetRepositoryGuidance()
	if !strings.Contains(g, strings.Repeat("a", 10)) {
		t.Fatalf("expected first 10 bytes present: %q", g)
	}
	if strings.Contains(g, strings.Repeat("a", 11)) {
		t.Fatalf("must not exceed the byte cap: %q", g)
	}
	if !strings.Contains(g, "[truncated") {
		t.Fatalf("truncated content must carry the truncation notice: %q", g)
	}
}

func TestRepositoryGuidance_InstructionContent_ExactCapNotTruncated(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("b", 10)
	mustWrite(t, filepath.Join(root, "AGENTS.md"), content)
	rc := &RepositoryContext{LocalPath: root, InstructionFileMaxBytes: 10}
	g := rc.GetRepositoryGuidance()
	if strings.Contains(g, "[truncated") {
		t.Fatalf("a file exactly at the cap must not be marked truncated: %q", g)
	}
}

func TestRepositoryGuidance_InstructionContent_SymlinkedFileSkipped(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.md"), "# outside the repo")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	// A real CLAUDE.md sits alongside the symlinked AGENTS.md so we can also
	// confirm detection falls through to the next priority match rather than
	// silently dropping instruction files whenever a symlink is present.
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# real claude instructions")

	rc := &RepositoryContext{LocalPath: root, InstructionFileMaxBytes: 8000}
	g := rc.GetRepositoryGuidance()
	if strings.Contains(g, "outside the repo") {
		t.Fatalf("must never read through a symlinked instruction file: %q", g)
	}
	if strings.Contains(g, "AGENTS.md") {
		t.Fatalf("symlinked AGENTS.md must not even be detected: %q", g)
	}
	if !strings.Contains(g, "real claude instructions") {
		t.Fatalf("detection must fall through to the real CLAUDE.md: %q", g)
	}
}

// TestRepositoryHeaderGuidance_ExcludesInstructionContent guards the split
// that error_rca_agent.go's complexity heuristic relies on: it greps
// RepositoryHeaderGuidance (not GetRepositoryGuidance) for "multiple" /
// "services" / "microservice" specifically so an analyzed repo's own
// AGENTS.md/CLAUDE.md prose — which routinely contains those words — can't
// make the heuristic fire on every repo regardless of actual complexity.
func TestRepositoryHeaderGuidance_ExcludesInstructionContent(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "This repo has multiple interconnected microservices.")
	rc := &RepositoryContext{LocalPath: root, InstructionFileMaxBytes: 8000}

	full := rc.GetRepositoryGuidance()
	if !strings.Contains(full, "microservices") {
		t.Fatalf("GetRepositoryGuidance must still include instruction-file content: %q", full)
	}

	header := rc.RepositoryHeaderGuidance()
	if strings.Contains(strings.ToLower(header), "microservice") || strings.Contains(strings.ToLower(header), "multiple") {
		t.Fatalf("RepositoryHeaderGuidance must not leak instruction-file content: %q", header)
	}
}
