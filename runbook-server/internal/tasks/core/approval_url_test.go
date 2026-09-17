package core

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The approval email link must target the canonical frontend route
// (app/src/pages/automation/approvals/[token].tsx), not the legacy /workflow path.
func TestBuildApprovalURL_UsesAutomationRouteAndKeepsSignature(t *testing.T) {
	link := buildApprovalURL("https://app.example.com/", "tok 1", "approved", "a@b.c", "key")

	u, err := url.Parse(link)
	require.NoError(t, err)
	assert.Equal(t, "/automation/approvals/tok 1", u.Path)
	assert.False(t, strings.Contains(link, "//automation"), "trailing slash on base must be trimmed: %s", link)
	assert.Equal(t, "approved", u.Query().Get("status"))
	assert.Equal(t, "a@b.c", u.Query().Get("approver"))
	assert.Equal(t, approvalSignature("tok 1", "approved", "a@b.c", "key"), u.Query().Get("sig"))
}
