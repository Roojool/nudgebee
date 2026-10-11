package agents

import (
	"strings"

	"nudgebee/code-analysis-agent/common"
	"nudgebee/code-analysis-agent/internal/session"
	"nudgebee/code-analysis-agent/planners"
)

// repoScopedPromptContext computes the repo-instructions/skills block for a
// specialist's prompt at construction time, and logs what was assembled.
// Shared by CodeAgent, ErrorRCAAgent, PerformanceDebuggerAgent and
// SecurityAuditorAgent, which each carried an identical copy of this block —
// this only covers the pre-clone case (LocalPath already known, e.g. a
// pre-supplied repository_path); the post-clone backfill for the common
// remote-URL case lives in planners.ReActPlanner.injectRepoInstructionsAfterClone.
//
// Explore mode is read-only Q&A — it never needs CLAUDE.md/AGENTS.md
// instruction-file content or repo skills (no fix/PR to guide), so both are
// skipped. The generic repo header (URL/branch/working scope) is still
// included in every mode: callers must not gate the call to this function
// itself on mode, or they'd lose that header too — this is the single place
// that decides what explore mode gets.
func repoScopedPromptContext(sessionCtx *session.SessionContext, logger *common.Logger) (contextInfo string, repoSkills string) {
	if sessionCtx.RepoContext == nil {
		return "", ""
	}

	mode := effectiveMode(sessionCtx)

	contextInfo = sessionCtx.RepoContext.RepositoryHeaderGuidance()

	var discoveredSkills []planners.RepoSkill
	var skillNames []string
	skillsLoaded := mode != ModeExplore
	if skillsLoaded {
		contextInfo += sessionCtx.RepoContext.InstructionFileGuidance()
		discoveredSkills = planners.DiscoverRepoSkills(sessionCtx.RepoContext.LocalPath)
		repoSkills = planners.FormatRepoSkillsBlock(discoveredSkills)
		for _, s := range discoveredSkills {
			skillNames = append(skillNames, s.Name)
		}
	}

	logger.Log(common.EventStepComplete, "repo-scoped context assembled", map[string]any{
		"local_path_known":           sessionCtx.RepoContext.LocalPath != "",
		"code_agent_mode":            mode,
		"skills_loaded":              skillsLoaded,
		"repo_instructions_injected": strings.Contains(contextInfo, "<repository-instructions"),
		"repo_skills_found":          len(discoveredSkills),
		"repo_skill_names":           skillNames,
	})
	return contextInfo, repoSkills
}

// effectiveMode is sessionCtx.Mode defaulted to ModeExplore when unset,
// shared by every specialist's prompt builder so the default lives in one
// place instead of being copy-pasted per agent.
func effectiveMode(sessionCtx *session.SessionContext) string {
	if sessionCtx.Mode == "" {
		return ModeExplore
	}
	return sessionCtx.Mode
}
