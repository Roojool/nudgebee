package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/llm/security"
	toolcore "nudgebee/llm/tools/core"
)

func TestResolveSessionRequestConfigs_WithoutHistorySurvivesWithConfig(t *testing.T) {
	labels := map[string]any{"subject": "k8s-collector-worker"}

	before := resolveSessionRequestConfigs(
		ConversationSessionRequestWithoutConversationHistory(),
		ConversationSessionRequestWithConfig(toolcore.NBQueryConfig{Labels: labels}),
	)
	after := resolveSessionRequestConfigs(
		ConversationSessionRequestWithConfig(toolcore.NBQueryConfig{Labels: labels}),
		ConversationSessionRequestWithoutConversationHistory(),
	)

	for name, cfg := range map[string]additionalConversationSessionRequestConfig{"before": before, "after": after} {
		assert.True(t, cfg.config.SkipConversationHistory, "option placed %s WithConfig was dropped", name)
		assert.Equal(t, labels, cfg.config.Labels, "option placed %s WithConfig", name)
	}
	assert.False(t, resolveSessionRequestConfigs().config.SkipConversationHistory)
}

// panicHistoryDao fails the test if history is read at all.
type panicHistoryDao struct {
	IConversationDao
	t *testing.T
}

func (d *panicHistoryDao) LoadConversationMessages(_, _, _ string, _ MessageType, _ int) ([]map[string]string, error) {
	d.t.Fatal("history loaded for a turn that asked to skip it")
	return nil, nil
}

func TestGetExistingHistory_SkipsLoadWhenRequested(t *testing.T) {
	original := GetConversationDao()
	defer SetConversationDao(original)
	SetConversationDao(&panicHistoryDao{t: t})

	history, err := getExistingHistory(NBAgentRequest{
		ConversationId: "conv",
		QueryConfig:    toolcore.NBQueryConfig{SkipConversationHistory: true},
	}, security.NewRequestContextForSuperAdmin())

	require.NoError(t, err)
	assert.Empty(t, history)
}

// storedConfigDao returns one stored message whose message_config is set.
type storedConfigDao struct {
	IConversationDao
	config string
}

func (d *storedConfigDao) GetConversationMessage(_, _, _ string) (ConversationMessage, error) {
	return ConversationMessage{MessageConfig: &d.config}, nil
}

func TestMergeLatestMessageConfig_ResumeKeepsSkipHistory(t *testing.T) {
	// An approval resume rebuilds the request with a non-empty config and
	// merges the stored message_config into it; the flag must come back.
	dao := &storedConfigDao{config: `{"skip_conversation_history":true,"event_id":"e1"}`}

	merged := mergeLatestMessageConfig(dao, "m", "a", "c",
		toolcore.NBQueryConfig{Namespace: "nudgebee"}, security.NewRequestContextForSuperAdmin().GetLogger(), "test")

	assert.True(t, merged.SkipConversationHistory)
	assert.Equal(t, "nudgebee", merged.Namespace)
}
