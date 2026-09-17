package agents

import (
	"context"
	"strings"
	"testing"

	"nudgebee/code-analysis-agent/common"
	"nudgebee/code-analysis-agent/planners"
)

const prSkillMarker = "NB-TEST-PR-SKILL: PRs must target the develop branch and include a Rollback section"

func skillsXMLBlock() string {
	return "<skills><skill name=\"create-pr\">" + prSkillMarker + "</skill></skills>"
}

func TestOperatorSkillsPromptBlock(t *testing.T) {
	// Empty / whitespace skills produce no block so prompts stay unchanged.
	for _, empty := range []string{"", "   ", "\n\t"} {
		if got := operatorSkillsPromptBlock(empty); got != "" {
			t.Errorf("operatorSkillsPromptBlock(%q) = %q, want empty", empty, got)
		}
	}

	block := operatorSkillsPromptBlock(skillsXMLBlock())
	if !strings.Contains(block, "<operator-skills>") {
		t.Errorf("expected wrapper tag, got: %s", block)
	}
	if !strings.Contains(block, prSkillMarker) {
		t.Errorf("expected skill body inlined, got: %s", block)
	}
}

func TestSanitizeBranchRef(t *testing.T) {
	cases := map[string]string{
		"feat/Add Cool Thing!":  "feat/add-cool-thing",
		"   Fix: NPE in foo   ": "fix-npe-in-foo",
		"!!!":                   "",
		"already-valid/slug":    "already-valid/slug",
	}
	for in, want := range cases {
		if got := sanitizeBranchRef(in); got != want {
			t.Errorf("sanitizeBranchRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateBranchNameDeterministicWithoutSkills(t *testing.T) {
	// With no skills the LLM path is skipped entirely, so a nil llmClient is fine.
	a := &OrchestratorAgent{}
	name := a.generateBranchName(context.Background(), "Fix the broken thing", "")
	if !strings.HasPrefix(name, "fix/") {
		t.Errorf("expected deterministic 'fix/' prefix, got %q", name)
	}
	if !strings.Contains(name, "fix-the-broken-thing") {
		t.Errorf("expected slug from title, got %q", name)
	}
}

func TestBuildCommitHeredocAvoidsDelimiterCollision(t *testing.T) {
	// Normal message uses the default delimiter.
	normal := buildCommitHeredoc("fix: a thing\n\nbody line")
	if !strings.Contains(normal, "<<'EOF_COMMIT_MSG'\n") || !strings.HasSuffix(normal, "\nEOF_COMMIT_MSG") {
		t.Errorf("expected default delimiter for normal message, got:\n%s", normal)
	}

	// A message containing the delimiter as its own line must force a different
	// delimiter so the heredoc is not terminated early.
	colliding := "fix: thing\nEOF_COMMIT_MSG\nmore body"
	out := buildCommitHeredoc(colliding)
	if strings.Contains(out, "<<'EOF_COMMIT_MSG'\n") {
		t.Errorf("delimiter collided with message body; heredoc would truncate:\n%s", out)
	}
	if !strings.Contains(out, colliding) {
		t.Errorf("commit message body must be preserved verbatim, got:\n%s", out)
	}
}

const repoSkillMarker = "NB-TEST-REPO-SKILL: use type(scope): subject"

func TestRepoSkillPromptBlock(t *testing.T) {
	if got := repoSkillPromptBlock(nil); got != "" {
		t.Errorf("repoSkillPromptBlock(nil) = %q, want empty", got)
	}

	skill := &planners.RepoSkill{Name: "commit", Path: ".claude/skills/commit/SKILL.md", Content: repoSkillMarker}
	block := repoSkillPromptBlock(skill)
	if !strings.Contains(block, "<repo-skill path=\".claude/skills/commit/SKILL.md\">") {
		t.Errorf("expected wrapper tag with path, got: %s", block)
	}
	if !strings.Contains(block, repoSkillMarker) {
		t.Errorf("expected skill body inlined, got: %s", block)
	}
	if !strings.Contains(block, "advisory documentation") {
		t.Errorf("expected non-authoritative framing (repo content, not operator-verified), got: %s", block)
	}
}

func TestRepoSkillPromptBlock_MarksTruncatedContent(t *testing.T) {
	// classifyRepoSkillForStep normally clears Truncated after re-reading the
	// winning skill in full; this covers that re-read's rare failure path,
	// where the discovery-time capped Content is all that's left to inject —
	// the model must be told it's a partial excerpt, not the whole file.
	full := repoSkillPromptBlock(&planners.RepoSkill{Name: "commit", Path: ".claude/skills/commit/SKILL.md", Content: repoSkillMarker, Truncated: false})
	if strings.Contains(full, "truncated") {
		t.Errorf("untruncated skill must not carry a truncation note, got: %s", full)
	}

	partial := repoSkillPromptBlock(&planners.RepoSkill{Name: "commit", Path: ".claude/skills/commit/SKILL.md", Content: repoSkillMarker, Truncated: true})
	if !strings.Contains(partial, "truncated") {
		t.Errorf("truncated skill must carry a visible truncation note, got: %s", partial)
	}
}

func TestBuildRepoSkillClassificationPrompt(t *testing.T) {
	skills := []planners.RepoSkill{
		{Name: "open-pr", Content: "---\ndescription: Create a pull request with our conventions\n---\n# body"},
		{Name: "deploy", Content: "how we deploy to prod"},
	}
	prompt := buildRepoSkillClassificationPrompt(skills, "writing pull request descriptions")

	for _, want := range []string{`"open-pr"`, `"deploy"`, "Create a pull request with our conventions", "writing pull request descriptions", "NONE"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
}

func TestBuildRepoSkillClassificationPrompt_TruncatesLongExcerpts(t *testing.T) {
	skills := []planners.RepoSkill{{Name: "big", Content: strings.Repeat("x", maxRepoSkillClassificationExcerpt+500)}}
	prompt := buildRepoSkillClassificationPrompt(skills, "some step")
	if strings.Contains(prompt, strings.Repeat("x", maxRepoSkillClassificationExcerpt+1)) {
		t.Errorf("expected excerpt to be capped at %d chars, got a longer run of x's", maxRepoSkillClassificationExcerpt)
	}
}

func TestParseRepoSkillClassificationAnswer(t *testing.T) {
	skills := []planners.RepoSkill{
		{Name: "open-pr", Content: "pr skill"},
		{Name: "commit", Content: "commit skill"},
	}

	cases := []struct {
		answer string
		want   string // "" means expect nil
	}{
		{`"open-pr"`, "open-pr"},
		{"open-pr", "open-pr"},
		{"  `commit`  \n", "commit"},
		{"NONE", ""},
		{"none", ""},
		{"", ""},
		{`"does-not-exist"`, ""}, // hallucinated name must not be trusted
	}
	for _, c := range cases {
		got := parseRepoSkillClassificationAnswer(c.answer, skills)
		if c.want == "" {
			if got != nil {
				t.Errorf("parseRepoSkillClassificationAnswer(%q) = %+v, want nil", c.answer, got)
			}
			continue
		}
		if got == nil || got.Name != c.want {
			t.Errorf("parseRepoSkillClassificationAnswer(%q) = %+v, want %q", c.answer, got, c.want)
		}
	}
}

func TestClassifyRepoSkillForStep_NoSkillsSkipsLLMCall(t *testing.T) {
	// A nil llmClient would panic if GenerateContent were ever called, so this
	// proves the "no repo skills at all" case short-circuits before the LLM.
	a := &OrchestratorAgent{}
	if got := a.classifyRepoSkillForStep(context.Background(), t.TempDir(), "writing commit messages"); got != nil {
		t.Errorf("expected nil with no repo skills present, got %+v", got)
	}
}

// TestClassifyRepoSkillForStep_NoSkillsRecordsNoVisibleStep ensures a repo
// with no committed skills doesn't clutter the UI's step list with an empty
// "classify_repo_skill" entry — the visible step should only appear when
// there was something to classify.
func TestClassifyRepoSkillForStep_NoSkillsRecordsNoVisibleStep(t *testing.T) {
	a := &OrchestratorAgent{toolTracker: common.NewToolInvocationTracker("test-analysis")}
	a.classifyRepoSkillForStep(context.Background(), t.TempDir(), "writing commit messages")
	if got := a.toolTracker.GetInvocations(); len(got) != 0 {
		t.Errorf("expected no tracked steps when the repo has no skills, got %+v", got)
	}
}

func TestPatternDetectionPromptIncludesSkills(t *testing.T) {
	a := &OrchestratorAgent{}

	withSkills := a.buildPatternDetectionPrompt(nil, nil, "fix thing", "desc", skillsXMLBlock())
	if !strings.Contains(withSkills, "<operator-skills>") || !strings.Contains(withSkills, prSkillMarker) {
		t.Errorf("pattern-detection prompt should embed operator skills when present")
	}

	without := a.buildPatternDetectionPrompt(nil, nil, "fix thing", "desc", "")
	if strings.Contains(without, "<operator-skills>") {
		t.Errorf("pattern-detection prompt must not emit an empty skills block")
	}
}
