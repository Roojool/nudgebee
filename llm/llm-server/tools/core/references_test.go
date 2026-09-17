package core

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"

	"nudgebee/llm/config"
)

func TestBuildAutomationURL(t *testing.T) {
	prev := config.Config.BaseUrl
	t.Cleanup(func() { config.Config.BaseUrl = prev })

	tests := []struct {
		name       string
		baseUrl    string
		accountId  string
		workflowId string
		extra      url.Values
		fragment   string
		want       string
	}{
		{"canonical singular path with accountId", "https://app.example.com", "acct-1", "wf-1", nil, "",
			"https://app.example.com/automation/wf-1?accountId=acct-1"},
		{"trailing slash on base is trimmed", "https://app.example.com/", "acct-1", "wf-1", nil, "",
			"https://app.example.com/automation/wf-1?accountId=acct-1"},
		{"id is path-escaped", "https://app.example.com", "acct-1", "wf with space", nil, "",
			"https://app.example.com/automation/wf%20with%20space?accountId=acct-1"},
		{"extra query and fragment, accountId wins", "https://app.example.com", "acct-1", "wf-1",
			url.Values{"session_id": {"s-1"}, "accountId": {"other"}}, "editor",
			"https://app.example.com/automation/wf-1?accountId=acct-1&session_id=s-1#editor"},
		{"empty accountId", "https://app.example.com", "", "wf-1", nil, "", ""},
		{"empty workflowId", "https://app.example.com", "acct-1", "", nil, "", ""},
		{"empty base url", "", "acct-1", "wf-1", nil, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config.Config.BaseUrl = tc.baseUrl
			assert.Equal(t, tc.want, BuildAutomationURL(tc.accountId, tc.workflowId, tc.extra, tc.fragment))
		})
	}
}

func TestBuildAutomationURLDoesNotMutateExtraQuery(t *testing.T) {
	prev := config.Config.BaseUrl
	config.Config.BaseUrl = "https://app.example.com"
	t.Cleanup(func() { config.Config.BaseUrl = prev })

	extra := url.Values{"session_id": {"s-1"}}
	BuildAutomationURL("acct-1", "wf-1", extra, "")
	assert.Equal(t, url.Values{"session_id": {"s-1"}}, extra)
}
