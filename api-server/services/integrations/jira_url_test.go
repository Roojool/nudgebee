package integrations

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nudgebee/services/integrations/core"
)

// The same stored value must resolve identically in api-server, ticket-server
// and llm-server; keep this table in sync with their copies.
func TestNormalizeJiraURL(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"company.atlassian.net", "https://company.atlassian.net"},
		{"https://company.atlassian.net", "https://company.atlassian.net"},
		{"http://jira.internal:8080", "http://jira.internal:8080"},
		{"jira.company.com/jira", "https://jira.company.com/jira"},
		{"https://jira.company.com/jira/", "https://jira.company.com/jira"},
		{"HTTPS://Jira.Company.com", "https://Jira.Company.com"},
		{"  company.atlassian.net/  ", "https://company.atlassian.net"},
	} {
		if got := normalizeJiraURL(tt.in); got != tt.want {
			t.Errorf("normalizeJiraURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestJiraValidateConfig_RejectsUnparsableURL(t *testing.T) {
	errs := Jira{}.ValidateConfig(nil, []core.IntegrationConfigValue{
		{Name: JiraConfigUrl, Value: "http://[::1"},
		{Name: JiraConfigUsername, Value: "u"},
		{Name: JiraConfigPassword, Value: "p"},
	}, "acct")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "not a valid address") {
		t.Fatalf("expected a specific URL error, got %v", errs)
	}
}

// Saving a Jira served over plain http under a context path validates against
// <base>/rest/api/2/project, not against the bare host.
func TestJiraValidateConfig_HTTPContextPath(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	errs := Jira{}.ValidateConfig(nil, []core.IntegrationConfigValue{
		{Name: JiraConfigUrl, Value: ts.URL + "/jira/"},
		{Name: JiraConfigUsername, Value: "u"},
		{Name: JiraConfigPassword, Value: "p"},
	}, "acct")
	if len(errs) != 0 {
		t.Fatalf("expected the http:// context-path URL to validate, got %v", errs)
	}
	if gotPath != "/jira/rest/api/2/project" {
		t.Fatalf("validation called %q, want /jira/rest/api/2/project", gotPath)
	}
}
