package core

import (
	"testing"

	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
)

// A wrong-target answer is well-formed and grounded in real numbers from the
// wrong system, so nothing downstream notices. Recording which config answered,
// and how it was chosen, is what makes it visible.
func TestWithResolvedToolTarget(t *testing.T) {
	t.Run("the executing context records which target ran", func(t *testing.T) {
		out := withExecutedToolTarget(nil, "dev-pg")
		assert.Equal(t, "dev-pg", out["tool_config_name"])
	})

	t.Run("a tool needing no config records nothing", func(t *testing.T) {
		assert.Nil(t, withExecutedToolTarget(nil, ""))
	})

	t.Run("the strategy is added only to a call that recorded a target", func(t *testing.T) {
		qc := toolcore.NBQueryConfig{
			ToolConfigs: map[string]string{"postgres_query_execute": "dev-pg"},
			ToolConfigMetadata: map[string]any{
				"postgres_query_execute": map[string]any{"strategy": "tool_identify"},
			},
		}
		ran := withExecutedToolTarget(map[string]any{"existing": 1}, "dev-pg")
		out := withResolvedToolTarget(ran, qc, "postgres_query_execute")
		assert.Equal(t, "dev-pg", out["tool_config_name"])
		assert.Equal(t, "tool_identify", out["tool_config_strategy"])
		assert.Equal(t, 1, out["existing"], "must not drop details the tool already reported")
	})

	// A parent orchestrator's map can name a config its call never used. Adding
	// that name here would label the wrong target as the one that ran.
	t.Run("a stale request map never invents a target", func(t *testing.T) {
		qc := toolcore.NBQueryConfig{ToolConfigs: map[string]string{"postgres_query_execute": "dev-pg"}}
		assert.Nil(t, withResolvedToolTarget(nil, qc, "postgres_query_execute"))
	})
}
