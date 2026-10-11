package planners

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nudgebee/code-analysis-agent/common"
)

// TestInjectRepoInstructionsAfterClone_BackfillsAndInjects is the regression
// test for the gap where a repo's AGENTS.md/CLAUDE.md and .claude/skills were
// never picked up in the common case: the specialist's system prompt is built
// once, before Plan() starts, and LocalPath is only known upfront when the
// caller pre-supplies a repository_path — otherwise the agent discovers and
// clones the repo itself as its first ReAct step, so the injection at prompt-
// build time always saw an empty LocalPath and skipped it. This exercises the
// mid-run backfill on the repo_clone step's own observation instead.
func TestInjectRepoInstructionsAfterClone_BackfillsAndInjects(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# build: make check"), 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, ".claude", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# deploy steps"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &RepositoryContext{URL: "https://example.com/o/r.git", InstructionFileMaxBytes: 8000}
	p := &ReActPlanner{repositoryContext: rc}

	observation := "Repository has 3 files ..."
	fields := p.injectRepoInstructionsAfterClone(root, &observation)

	if fields == nil {
		t.Fatal("expected non-nil fields when LocalPath was previously unknown")
	}
	if rc.LocalPath != root {
		t.Fatalf("LocalPath was not backfilled: got %q, want %q", rc.LocalPath, root)
	}
	if !strings.Contains(observation, "<repository-instructions") {
		t.Fatalf("observation missing injected instructions: %q", observation)
	}
	if !strings.Contains(observation, "deploy steps") {
		t.Fatalf("observation missing injected repo skill: %q", observation)
	}
	if got, ok := fields["repo_instructions_injected"].(bool); !ok || !got {
		t.Fatalf("fields.repo_instructions_injected = %v, want true", fields["repo_instructions_injected"])
	}
	if got, ok := fields["repo_skills_found"].(int); !ok || got != 1 {
		t.Fatalf("fields.repo_skills_found = %v, want 1", fields["repo_skills_found"])
	}
	if got, ok := fields["instruction_files_found"].([]string); !ok || len(got) != 1 || got[0] != "CLAUDE.md" {
		t.Fatalf("fields.instruction_files_found = %v, want [CLAUDE.md]", fields["instruction_files_found"])
	}
	if got, ok := fields["repo_skill_names"].([]string); !ok || len(got) != 1 || got[0] != "deploy" {
		t.Fatalf("fields.repo_skill_names = %v, want [deploy]", fields["repo_skill_names"])
	}
}

// TestRecordRepoContextStep_RecordsVisibleStep is the regression test for
// making repo-instructions/skills loading show up as its own visible step
// (a tool_invocations entry) in the analysis UI, the same way repo_clone or
// file_view do — previously this only ever appeared inside a structured log
// field, invisible to anyone not reading raw logs.
func TestRecordRepoContextStep_RecordsVisibleStep(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# build: make check"), 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, ".claude", "skills", "create-pr")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# create pr steps"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &RepositoryContext{URL: "https://example.com/o/r.git", InstructionFileMaxBytes: 8000}
	tracker := common.NewToolInvocationTracker("test-analysis")
	p := &ReActPlanner{repositoryContext: rc, toolTracker: tracker}

	observation := "Repository has 3 files ..."
	fields := p.injectRepoInstructionsAfterClone(root, &observation)
	if fields == nil {
		t.Fatal("expected non-nil fields")
	}
	p.recordRepoContextStep(root, fields)

	invocations := tracker.GetInvocations()
	if len(invocations) != 1 {
		t.Fatalf("expected exactly one recorded step, got %d: %+v", len(invocations), invocations)
	}
	inv := invocations[0]
	if inv.ToolName != "load_repo_context" {
		t.Fatalf("ToolName = %q, want load_repo_context", inv.ToolName)
	}
	if inv.Status != "success" {
		t.Fatalf("Status = %q, want success", inv.Status)
	}
	summary, ok := inv.Output.(string)
	if !ok {
		t.Fatalf("expected string Output, got %T: %v", inv.Output, inv.Output)
	}
	if !strings.Contains(summary, "AGENTS.md") {
		t.Errorf("expected summary to mention AGENTS.md, got: %s", summary)
	}
	if !strings.Contains(summary, "create-pr") {
		t.Errorf("expected summary to mention the discovered skill name, got: %s", summary)
	}
}

// TestRecordRepoContextStep_NilTrackerIsNoop covers analysis modes that never
// set a tool tracker (e.g. direct unit construction) — must not panic.
func TestRecordRepoContextStep_NilTrackerIsNoop(t *testing.T) {
	p := &ReActPlanner{repositoryContext: &RepositoryContext{}}
	p.recordRepoContextStep("/tmp/whatever", map[string]any{
		"instruction_files_found": []string{"CLAUDE.md"},
		"repo_skill_names":        []string{"deploy"},
	})
}

// TestInjectRepoInstructionsAfterClone_NoopWhenLocalPathAlreadyKnown covers
// the pre-resolved repository_path case: the system prompt built at Plan()
// start already carried this content, so injecting it again on repo_clone
// would just duplicate it in the transcript.
func TestInjectRepoInstructionsAfterClone_NoopWhenLocalPathAlreadyKnown(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# build: make check"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &RepositoryContext{URL: "https://example.com/o/r.git", LocalPath: root, InstructionFileMaxBytes: 8000}
	p := &ReActPlanner{repositoryContext: rc}

	observation := "Repository has 3 files ..."
	fields := p.injectRepoInstructionsAfterClone(root, &observation)

	if fields != nil {
		t.Fatalf("expected nil fields when LocalPath was already known, got %v", fields)
	}
	if observation != "Repository has 3 files ..." {
		t.Fatalf("observation must be untouched, got %q", observation)
	}
}

// TestInjectRepoInstructionsAfterClone_NilRepositoryContext covers the
// log-only analysis path (no repository at all).
func TestInjectRepoInstructionsAfterClone_NilRepositoryContext(t *testing.T) {
	p := &ReActPlanner{}
	observation := "unchanged"
	if fields := p.injectRepoInstructionsAfterClone(t.TempDir(), &observation); fields != nil {
		t.Fatalf("expected nil fields with no repository context, got %v", fields)
	}
	if observation != "unchanged" {
		t.Fatalf("observation must be untouched, got %q", observation)
	}
}
