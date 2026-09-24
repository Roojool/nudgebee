package agents

import (
	"errors"
	"sync"
	"testing"

	"nudgebee/llm/agents/core"
	"nudgebee/llm/common"
	"nudgebee/llm/security"
	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoDao embeds IConversationDao (nil) and overrides only the write the code
// agent makes to remember its repo; any other method would panic.
type repoDao struct {
	core.IConversationDao
	mu    sync.Mutex
	saved []savedRepo
	err   error
}

type savedRepo struct{ messageId, repo string }

func (d *repoDao) UpdateConversationMessageGitRepo(id, gitRepo string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.saved = append(d.saved, savedRepo{messageId: id, repo: gitRepo})
	return d.err
}

func withRepoDao(t *testing.T, d *repoDao) {
	t.Helper()
	prev := core.GetConversationDao()
	core.SetConversationDao(d)
	t.Cleanup(func() { core.SetConversationDao(prev) })
}

// Regression for #38547: a plain follow-up such as "now raise a pr for this"
// names no repo. The code agent must take it from the conversation's stored
// config (inherited from the earlier turn) rather than detect one again, and it
// must remember it on this turn's message so the turn after that inherits it too.
func TestFollowupTakesRepoFromQueryConfigAndRemembersIt(t *testing.T) {
	const repoA = "https://github.com/acme/service-a"
	d := &repoDao{}
	withRepoDao(t, d)

	query := core.NBAgentRequest{
		Query:     "now raise a pr for this",
		MessageId: "msg-3",
		QueryConfig: toolcore.NBQueryConfig{
			GitRepo: repoA, // what conversation.go inherits from the previous message
		},
	}

	// Execute's first steps for a non-JSON tool input: the request carries no repo.
	req := CodeAgent2Request{}
	require.Error(t, common.UnmarshalJson([]byte(query.Query), &req), "plain text is not a JSON tool input")
	require.Empty(t, req.GitRepo)
	require.Empty(t, extractGitURLFromText(query.Query), "the follow-up text names no repo")

	req.GitRepo = repoFromQueryConfig(query.QueryConfig)
	assert.Equal(t, repoA, req.GitRepo)

	rememberConversationRepo(security.NewRequestContextForSuperAdmin(), query.MessageId, req.GitRepo)
	assert.Equal(t, []savedRepo{{messageId: "msg-3", repo: repoA}}, d.saved)
}

func TestRepoFromQueryConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  toolcore.NBQueryConfig
		want string
	}{
		{"nothing stored", toolcore.NBQueryConfig{}, ""},
		{"stored on the message config", toolcore.NBQueryConfig{GitRepo: "https://github.com/acme/a"}, "https://github.com/acme/a"},
		{
			"a followup selection beats the stored repo",
			toolcore.NBQueryConfig{
				GitRepo:     "https://github.com/acme/a",
				ToolConfigs: map[string]string{"git_repo": "https://github.com/acme/b"},
			},
			"https://github.com/acme/b",
		},
		{
			"other tool configs are not a repo",
			toolcore.NBQueryConfig{GitRepo: "https://github.com/acme/a", ToolConfigs: map[string]string{"prometheus": "prod"}},
			"https://github.com/acme/a",
		},
		{
			"an empty followup selection falls back to the stored repo",
			toolcore.NBQueryConfig{GitRepo: "https://github.com/acme/a", ToolConfigs: map[string]string{"git_repo": ""}},
			"https://github.com/acme/a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, repoFromQueryConfig(tt.cfg))
		})
	}
}

func TestRememberConversationRepo(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	t.Run("nothing to remember writes nothing", func(t *testing.T) {
		d := &repoDao{}
		withRepoDao(t, d)
		rememberConversationRepo(ctx, "msg-1", "")
		assert.Empty(t, d.saved)
	})

	t.Run("a failed write does not fail the turn", func(t *testing.T) {
		d := &repoDao{err: errors.New("db down")}
		withRepoDao(t, d)
		assert.NotPanics(t, func() { rememberConversationRepo(ctx, "msg-1", "https://github.com/acme/a") })
		assert.Len(t, d.saved, 1)
	})
}
