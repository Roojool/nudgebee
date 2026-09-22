package account

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var archiveUpdate = regexp.MustCompile(`(?is)update\s+cloud_resourses\s+set\s+is_active\s*=\s*false\s*,\s*status\s*=\s*'Deleted'\s+where`)

// Every archive UPDATE must carry notYetArchived, otherwise a sync that finds
// nothing rewrites each already-deleted row of the service on every run.
// Statements that already guard on `status != 'Deleted'` are exempt.
func TestArchiveUpdatesSkipRowsAlreadyDeleted(t *testing.T) {
	src, err := os.ReadFile("etl_resources.go")
	require.NoError(t, err)
	content := string(src)

	matches := archiveUpdate.FindAllStringIndex(content, -1)
	seen := 0
	for i, m := range matches {
		end := min(m[1]+300, len(content))
		if i+1 < len(matches) {
			end = min(end, matches[i+1][0])
		}
		tail := content[m[1]:end]
		if strings.Contains(tail, "status != 'Deleted'") {
			continue
		}
		seen++
		assert.Contains(t, tail, "notYetArchived", "unguarded archive UPDATE: %.120s", content[m[0]:m[1]])
	}
	assert.Equal(t, 6, seen, "archive UPDATE count changed; update this test with it")
}
