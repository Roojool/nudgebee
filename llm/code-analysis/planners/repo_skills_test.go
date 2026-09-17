package planners

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nudgebee/code-analysis-agent/common"
)

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverRepoSkills_MissingDirIsNilSafe(t *testing.T) {
	if got := DiscoverRepoSkills(t.TempDir()); got != nil {
		t.Fatalf("no skills dir must return nil, got %v", got)
	}
	if got := DiscoverRepoSkills(""); got != nil {
		t.Fatalf("empty root must return nil, got %v", got)
	}
}

func TestDiscoverRepoSkills_ClaudeWinsOverAgents(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "from-claude"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "from-claude", "SKILL.md"), "claude skill content")
	mustMkdirAll(t, filepath.Join(root, ".agents", "skills", "from-agents"))
	mustWrite(t, filepath.Join(root, ".agents", "skills", "from-agents", "SKILL.md"), "agents skill content")

	got := DiscoverRepoSkills(root)
	if len(got) != 1 || got[0].Name != "from-claude" {
		t.Fatalf("expected only .claude/skills to be read when both real dirs exist, got %+v", got)
	}
	if want := filepath.Join(".claude", "skills", "from-claude", "SKILL.md"); got[0].Path != want {
		t.Fatalf("Path must be repo-relative, want %q got %q", want, got[0].Path)
	}
}

func TestDiscoverRepoSkills_AgentsUsedWhenClaudeAbsent(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".agents", "skills", "only-here"))
	mustWrite(t, filepath.Join(root, ".agents", "skills", "only-here", "SKILL.md"), "content")

	got := DiscoverRepoSkills(root)
	if len(got) != 1 || got[0].Name != "only-here" {
		t.Fatalf("expected .agents/skills fallback, got %+v", got)
	}
}

func TestDiscoverRepoSkills_SymlinkPairResolvesToOneLoad(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "shared"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "shared", "SKILL.md"), "shared content")
	// .agents -> .claude, the common cross-tool-compatibility pattern.
	if err := os.Symlink(filepath.Join(root, ".claude"), filepath.Join(root, ".agents")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	got := DiscoverRepoSkills(root)
	if len(got) != 1 || got[0].Name != "shared" {
		t.Fatalf("symlinked .agents -> .claude must resolve to exactly one load, got %+v", got)
	}
}

// TestDiscoverRepoSkills_SymlinkedRepoRootProducesCleanRelativePath is the
// regression test for a real repoRootReal (macOS /var -> /private/var, or any
// workspace path reached through a symlinked parent directory): diffing the
// resolved skills root against the unresolved repoRoot used to produce a
// needlessly long ../.. relative path — or worse, one that leaks the real
// underlying directory structure — instead of the clean repo-relative label
// (".claude/skills/<name>/SKILL.md") every other case gets.
func TestDiscoverRepoSkills_SymlinkedRepoRootProducesCleanRelativePath(t *testing.T) {
	realRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(realRoot, ".claude", "skills", "deploy"))
	mustWrite(t, filepath.Join(realRoot, ".claude", "skills", "deploy", "SKILL.md"), "deploy steps")

	symlinkedRoot := filepath.Join(t.TempDir(), "repo-via-symlink")
	if err := os.Symlink(realRoot, symlinkedRoot); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	got := DiscoverRepoSkills(symlinkedRoot)
	if len(got) != 1 {
		t.Fatalf("expected exactly one skill discovered through the symlinked root, got %+v", got)
	}
	want := filepath.Join(".claude", "skills", "deploy", "SKILL.md")
	if got[0].Path != want {
		t.Fatalf("Path leaked the resolved host structure instead of a clean relative label: got %q, want %q", got[0].Path, want)
	}
}

func TestDiscoverRepoSkills_SymlinkEscapingRepoRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustMkdirAll(t, filepath.Join(outside, "skills", "evil"))
	mustWrite(t, filepath.Join(outside, "skills", "evil", "SKILL.md"), "should never be read")
	mustMkdirAll(t, root+"/.claude")
	if err := os.Symlink(filepath.Join(outside, "skills"), filepath.Join(root, ".claude", "skills")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	got := DiscoverRepoSkills(root)
	if len(got) != 0 {
		t.Fatalf("a skills dir symlinked outside the repo must be rejected, got %+v", got)
	}
}

func TestDiscoverRepoSkills_IndividualSymlinkedSkillFileRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "SKILL.md"), "should never be read")
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "legit"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "legit", "SKILL.md"), "real content")
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "symlinked"))
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(root, ".claude", "skills", "symlinked", "SKILL.md")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	got := DiscoverRepoSkills(root)
	if len(got) != 1 || got[0].Name != "legit" {
		t.Fatalf("symlinked SKILL.md must be rejected while the real one is still read, got %+v", got)
	}
}

func TestDiscoverRepoSkills_NoFileCountCap(t *testing.T) {
	root := t.TempDir()
	const skillCount = 30 // comfortably past the old maxSkillFiles=20 cap
	for i := range skillCount {
		dir := filepath.Join(root, ".claude", "skills", fmt.Sprintf("skill-%02d", i))
		mustMkdirAll(t, dir)
		mustWrite(t, filepath.Join(dir, "SKILL.md"), "content")
	}

	got := DiscoverRepoSkills(root)
	if len(got) != skillCount {
		t.Fatalf("expected all %d skills discovered (no count cap now that only a lightweight index is injected), got %d", skillCount, len(got))
	}
}

func TestDiscoverRepoSkills_CapsPerFileSize(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "big"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "big", "SKILL.md"), strings.Repeat("x", maxBytesPerSkillFile+500))
	got := DiscoverRepoSkills(root)
	if len(got) != 1 {
		t.Fatalf("expected the oversized skill to still load (truncated), got %+v", got)
	}
	if !got[0].Truncated {
		t.Fatalf("expected Truncated=true for an oversized skill file")
	}
	if len(got[0].Content) != maxBytesPerSkillFile {
		t.Fatalf("expected content capped at %d bytes, got %d", maxBytesPerSkillFile, len(got[0].Content))
	}
}

func TestFormatRepoSkillsBlock(t *testing.T) {
	if got := FormatRepoSkillsBlock(nil); got != "" {
		t.Fatalf("no skills must format to empty string, got %q", got)
	}
	block := FormatRepoSkillsBlock([]RepoSkill{{
		Name:        "deploy",
		Description: "how we deploy to prod",
		Path:        ".claude/skills/deploy/SKILL.md",
		Content:     "how we deploy to prod\n\n1. run make release\n2. tag the commit\n...",
	}})
	if !strings.Contains(block, "NOT operator-verified guidance") {
		t.Fatalf("block missing non-privileged framing: %q", block)
	}
	if !strings.Contains(block, "deploy: how we deploy to prod (.claude/skills/deploy/SKILL.md)") {
		t.Fatalf("block missing name+description+path index entry: %q", block)
	}
	if strings.Contains(block, "run make release") {
		t.Fatalf("block must not preload full skill content, only the index: %q", block)
	}

	noDesc := FormatRepoSkillsBlock([]RepoSkill{{Name: "bare", Path: ".claude/skills/bare/SKILL.md"}})
	if !strings.Contains(noDesc, "bare: (no description)") {
		t.Fatalf("skill with no description must still get an index entry: %q", noDesc)
	}
}

func TestSkillDescription_FromFrontmatter(t *testing.T) {
	content := "---\nname: deploy\ndescription: How we deploy to production\n---\n\n1. run make release\n"
	if got := skillDescription(content); got != "How we deploy to production" {
		t.Fatalf("expected frontmatter description, got %q", got)
	}
}

func TestSkillDescription_FallsBackToFirstBodyLine(t *testing.T) {
	if got := skillDescription("# Deploy Steps\n\nRun make release, then tag.\n"); got != "Deploy Steps" {
		t.Fatalf("expected first heading line as fallback, got %q", got)
	}
	// Frontmatter present but no description key — falls back to the body.
	content := "---\nname: deploy\n---\nRun make release.\n"
	if got := skillDescription(content); got != "Run make release." {
		t.Fatalf("expected body fallback when frontmatter has no description, got %q", got)
	}
}

func TestSkillDescription_TruncatesLongFallback(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := skillDescription(long)
	if !strings.HasSuffix(got, "…") || len(got) <= 150 {
		t.Fatalf("expected truncated fallback with ellipsis, got %q (len %d)", got, len(got))
	}
}

func TestSkillDescription_TruncatesLongFrontmatterValue(t *testing.T) {
	long := strings.Repeat("a", 700)
	content := "---\nname: console-audit\ndescription: " + long + "\n---\nbody\n"
	got := skillDescription(content)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected long frontmatter description to be truncated, got len %d", len(got))
	}
	if n := len([]rune(got)); n > 151 { // 150 chars + the ellipsis rune
		t.Fatalf("expected frontmatter description capped at 150 chars, got %d runes: %q", n, got)
	}
}

func TestSkillDescription_BlockScalarFallsBackToBody(t *testing.T) {
	for _, indicator := range []string{">", "|", ">-", "|-"} {
		content := "---\nname: deploy\ndescription: " + indicator + "\n  How we deploy, spread\n  across multiple lines.\n---\n# Deploy Steps\nbody\n"
		got := skillDescription(content)
		if got == indicator {
			t.Fatalf("block/folded scalar indicator %q must not be returned as the description verbatim", indicator)
		}
		if got != "Deploy Steps" {
			t.Fatalf("expected fallback to the body heading for indicator %q, got %q", indicator, got)
		}
	}
}

func TestDiscoverRepoSkills_PopulatesDescription(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "open-pr"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "open-pr", "SKILL.md"), "---\nname: open-pr\ndescription: How this team opens PRs\n---\nbody")

	got := DiscoverRepoSkills(root)
	if len(got) != 1 || got[0].Description != "How this team opens PRs" {
		t.Fatalf("expected parsed description, got %+v", got)
	}
}

func TestFindRepoSkillByName(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "open-pr"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "open-pr", "SKILL.md"), "how we create PRs")
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "git-commit"))
	mustWrite(t, filepath.Join(root, ".claude", "skills", "git-commit", "SKILL.md"), "how we commit")
	skills := DiscoverRepoSkills(root)

	got := FindRepoSkillByName(skills, "open-pr")
	if got == nil || got.Content != "how we create PRs" {
		t.Fatalf("expected open-pr skill to be found, got %+v", got)
	}
	if got := FindRepoSkillByName(skills, "git-commit"); got == nil || got.Content != "how we commit" {
		t.Fatalf("expected git-commit skill to be found, got %+v", got)
	}
	if got := FindRepoSkillByName(skills, "does-not-exist"); got != nil {
		t.Fatalf("expected nil for a skill name that doesn't exist, got %+v", got)
	}
	if got := FindRepoSkillByName(nil, "open-pr"); got != nil {
		t.Fatalf("expected nil against an empty skill list, got %+v", got)
	}
}

func TestAgentTemplates_CriticalConstraintsPrecedeRepoAndOperatorSkills(t *testing.T) {
	loader := common.NewPromptLoader()
	data := map[string]any{
		"ContextInfo":   "",
		"OriginalQuery": "why is this slow",
		"Mode":          "explore",
		"IsExploreMode": true,
		"IsFixMode":     false,
		"Skills":        "operator skill body",
		"RepoSkills":    "repo skill body",
	}
	// All four specialist agents share this contract: an operator/repo skill
	// block never precedes <critical-constraints> — the rules it must not
	// override. code_agent and error_rca previously got this backwards.
	for _, name := range []string{"code_agent", "error_rca", "performance_debugger", "security_auditor"} {
		t.Run(name, func(t *testing.T) {
			out, err := loader.LoadPrompt(name, data)
			if err != nil {
				t.Fatalf("LoadPrompt(%s) failed: %v", name, err)
			}
			if !strings.Contains(out, "<operator-skills>") || !strings.Contains(out, "operator skill body") {
				t.Fatalf("%s: rendered prompt missing operator-skills block: %q", name, out)
			}
			if !strings.Contains(out, "<repository-skills>") || !strings.Contains(out, "repo skill body") {
				t.Fatalf("%s: rendered prompt missing repository-skills block: %q", name, out)
			}
			if !strings.Contains(out, "higher priority") {
				t.Fatalf("%s: rendered prompt lost operator-skills trust framing: %q", name, out)
			}
			opIdx := strings.Index(out, "<operator-skills>")
			repoIdx := strings.Index(out, "<repository-skills>")
			if opIdx == -1 || repoIdx == -1 || opIdx > repoIdx {
				t.Fatalf("%s: operator-skills must render before repository-skills, got operator@%d repo@%d", name, opIdx, repoIdx)
			}

			// Regression guard: repo-sourced (untrusted) content and
			// operator-authored skills must render AFTER <critical-constraints>
			// — the operating rules they must not override — not before. An
			// earlier version of code_agent/error_rca put the skills block
			// first, so the constraints it must not override came after it in
			// the prompt instead of before.
			constraintsIdx := strings.Index(out, "<critical-constraints>")
			if constraintsIdx == -1 || constraintsIdx > opIdx || constraintsIdx > repoIdx {
				t.Fatalf("%s: <critical-constraints> must render before operator-skills/repository-skills, got constraints@%d operator@%d repo@%d", name, constraintsIdx, opIdx, repoIdx)
			}
		})
	}
}

func TestReadSkillFileFull_IgnoresDiscoveryTimeByteCap(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".claude", "skills", "big"))
	fullContent := "---\nname: big\ndescription: a big skill\n---\n" + strings.Repeat("a", maxBytesPerSkillFile+500)
	mustWrite(t, filepath.Join(root, ".claude", "skills", "big", "SKILL.md"), fullContent)

	skills := DiscoverRepoSkills(root)
	if len(skills) != 1 || !skills[0].Truncated {
		t.Fatalf("expected one truncated skill from discovery, got %+v", skills)
	}
	if len(skills[0].Content) != maxBytesPerSkillFile {
		t.Fatalf("expected discovery-time Content capped at %d bytes, got %d", maxBytesPerSkillFile, len(skills[0].Content))
	}

	full, err := ReadSkillFileFull(root, skills[0])
	if err != nil {
		t.Fatalf("ReadSkillFileFull failed: %v", err)
	}
	if full != fullContent {
		t.Fatalf("expected the full, uncapped file content, got %d bytes (want %d)", len(full), len(fullContent))
	}
}
