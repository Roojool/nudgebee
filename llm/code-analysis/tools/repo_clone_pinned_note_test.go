package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The planner feeds a tool's Observation to the model and reads Data only for
// local_path, so anything the model must act on has to be in the prose. A
// pinned clone that fails to say so leaves the agent diffing the deployed
// commit against itself and reporting "unchanged on the default branch".
func TestPinnedCheckoutNote(t *testing.T) {
	t.Run("names the ref that answers the question", func(t *testing.T) {
		note := pinnedCheckoutNote("742b42f8", "main")
		assert.Contains(t, note, "742b42f8")
		assert.Contains(t, note, "detached")
		assert.Contains(t, note, "always empty")
		assert.Contains(t, note, "origin/main")
		assert.Contains(t, note, "git diff 742b42f8 origin/main")
	})

	t.Run("still warns when the default branch could not be resolved", func(t *testing.T) {
		note := pinnedCheckoutNote("742b42f8", "")
		assert.Contains(t, note, "always empty")
		assert.NotContains(t, note, "origin/")
		assert.False(t, strings.Contains(note, "compare against"),
			"must not name a comparison ref it does not have")
	})
}
