package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The same stored value must resolve identically in api-server, ticket-server
// and llm-server; keep this table in sync with their copies.
func TestSanitizeURL_SharedJiraCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"company.atlassian.net", "https://company.atlassian.net"},
		{"https://company.atlassian.net", "https://company.atlassian.net"},
		{"http://jira.internal:8080", "http://jira.internal:8080"},
		{"jira.company.com/jira", "https://jira.company.com/jira"},
		{"https://jira.company.com/jira/", "https://jira.company.com/jira"},
		{"HTTPS://Jira.Company.com", "https://Jira.Company.com"},
		{"  company.atlassian.net/  ", "https://company.atlassian.net"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeURL(tc.in))
		})
	}
}
