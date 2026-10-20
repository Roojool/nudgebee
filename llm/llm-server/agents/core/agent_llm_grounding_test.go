package core

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Summary-tier LLM tool calls often arrive with an empty context while asking
// for environment data. The prompt must forbid inventing that data (a prod
// investigation reported a made-up cache hit rate and version bump), and both
// Summary-tier entry points must use it rather than a local "summarize the
// context" string.
func TestGroundedSummarySystemPrompt(t *testing.T) {
	for _, phrase := range []string{
		"must come from the context",
		"not in the context",
		"never estimate or invent",
		"First report the relevant data the context contains",
		"general knowledge",
	} {
		assert.Contains(t, groundedSummarySystemPrompt, phrase)
	}

	// agent_llm.go defines the constant (comment + declaration) and uses it once;
	// executor_planner.go only uses it. Counting occurrences, not mere presence,
	// catches a file that keeps the definition but stops using it.
	for file, minRefs := range map[string]int{"agent_llm.go": 3, "executor_planner.go": 1} {
		src, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.NotContains(t, string(src), "Provide a concise and accurate summary of the provided context",
			"%s must use groundedSummarySystemPrompt instead of an ungrounded summary prompt", file)
		assert.GreaterOrEqual(t, strings.Count(string(src), "groundedSummarySystemPrompt"), minRefs,
			"%s must reference groundedSummarySystemPrompt", file)
	}
}
