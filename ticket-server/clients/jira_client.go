package clients

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jira "github.com/andygrunwald/go-jira"
)

// jiraHTTPTimeout is the timeout for the Jira HTTP client.
const jiraHTTPTimeout = 15 * time.Second

// JiraAuthDataCenterPAT is the auth_type for Jira Data Center personal access
// tokens, which must be sent as a bearer token — Data Center rejects them over
// Basic with 401. Every other auth_type (including "token" and unset, which is
// what existing integrations store) uses Basic. Mirrors the api-server Jira
// integration schema (api-server/services/integrations/jira.go).
const JiraAuthDataCenterPAT = "datacenter_pat"

// IsJiraDataCenterPAT reports whether the stored auth_type is a Data Center PAT.
func IsJiraDataCenterPAT(authType string) bool {
	return strings.TrimSpace(authType) == JiraAuthDataCenterPAT
}

func CreateJiraClient(authType, username, password, rawURL string) (*jira.Client, error) {
	var ct *http.Client
	if IsJiraDataCenterPAT(authType) {
		tp := jira.BearerAuthTransport{Token: password}
		ct = tp.Client()
	} else {
		tp := jira.BasicAuthTransport{
			Username: username,
			Password: password,
		}
		ct = tp.Client()
	}
	ct.Timeout = jiraHTTPTimeout
	client, err := jira.NewClient(ct, NormalizeJiraURL(rawURL))
	if err != nil {
		return nil, err
	}

	return client, nil
}

// jiraDeploymentTTL bounds how long a detected deployment type is reused. The
// type of an instance does not change, so this only limits how long a wrong
// answer (e.g. a URL re-pointed at another instance) can linger.
const jiraDeploymentTTL = time.Hour

type jiraDeployment struct {
	cloud   bool
	expires time.Time
}

// jiraDeployments caches IsJiraCloud results by base URL. Failures are not cached.
var jiraDeployments sync.Map

// NormalizeJiraURL turns the stored Jira "url" config value into a base URL. The
// value is whatever the tenant typed: a bare host ("company.atlassian.net"),
// a host with a context path ("jira.company.com/jira"), or a full address with
// https:// or http://. A scheme is added only when none is present, so http://
// instances keep working, and trailing slashes are dropped. The result goes
// through url.Parse like api-server's normalizeJiraURL and llm-server's sanitizeURL,
// so the three produce identical output.
func NormalizeJiraURL(raw string) string {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	return strings.TrimRight(u.String(), "/")
}

// JiraBrowseURL returns the web link for an issue key on the client's instance,
// keeping the scheme and any context path of the base URL (https://host/jira/browse/KEY).
func JiraBrowseURL(client *jira.Client, key string) string {
	base := client.GetBaseURL()
	return base.ResolveReference(&url.URL{Path: "browse/" + key}).String()
}

// IsJiraCloud reports whether the instance is Jira Cloud rather than
// Server/Data Center. The two differ in user identity (accountId vs name),
// in which user-search parameters they accept, and in which endpoints exist.
// The answer is cached per base URL to avoid a serverInfo round-trip per call.
func IsJiraCloud(client *jira.Client) (bool, error) {
	baseURL := client.GetBaseURL()
	key := baseURL.String()
	if v, ok := jiraDeployments.Load(key); ok {
		d := v.(jiraDeployment)
		if time.Now().Before(d.expires) {
			return d.cloud, nil
		}
		jiraDeployments.Delete(key)
	}
	req, err := client.NewRequest("GET", "rest/api/2/serverInfo", nil)
	if err != nil {
		return false, err
	}
	var info struct {
		DeploymentType string `json:"deploymentType"`
	}
	if _, err := client.Do(req, &info); err != nil {
		return false, err
	}
	cloud := strings.EqualFold(info.DeploymentType, "Cloud")
	jiraDeployments.Store(key, jiraDeployment{cloud: cloud, expires: time.Now().Add(jiraDeploymentTTL)})
	return cloud, nil
}
