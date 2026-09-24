package clients

import (
	"net/http"
	"net/http/httptest"
	"testing"

	jira "github.com/andygrunwald/go-jira"
)

// Jira Data Center rejects personal access tokens over Basic with 401, so the
// datacenter_pat auth_type must send a bearer token; every other auth_type
// (including unset, which older integrations store) keeps Basic.
func TestCreateJiraClient_AuthHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"svc"}`))
	}))
	defer ts.Close()

	// The auth transports fall back to http.DefaultTransport; point it at the
	// test server's TLS config.
	orig := http.DefaultTransport
	http.DefaultTransport = ts.Client().Transport
	defer func() { http.DefaultTransport = orig }()

	tests := []struct {
		name, authType, username, want string
	}{
		{"datacenter pat uses bearer", JiraAuthDataCenterPAT, "", "Bearer secret-pat"},
		{"token uses basic", "token", "user", "Basic dXNlcjpzZWNyZXQtcGF0"},
		{"unset uses basic", "", "user", "Basic dXNlcjpzZWNyZXQtcGF0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAuth = ""
			client, err := CreateJiraClient(tt.authType, tt.username, "secret-pat", ts.URL)
			if err != nil {
				t.Fatalf("CreateJiraClient: %v", err)
			}
			if _, _, err := client.User.GetSelf(); err != nil {
				t.Fatalf("GetSelf: %v", err)
			}
			if gotAuth != tt.want {
				t.Fatalf("Authorization = %q, want %q", gotAuth, tt.want)
			}
		})
	}
}

func TestIsJiraCloud(t *testing.T) {
	for _, tt := range []struct {
		deploymentType string
		want           bool
	}{
		{"Cloud", true},
		{"Server", false},
	} {
		t.Run(tt.deploymentType, func(t *testing.T) {
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/rest/api/2/serverInfo" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"deploymentType":"` + tt.deploymentType + `"}`))
			}))
			defer ts.Close()
			orig := http.DefaultTransport
			http.DefaultTransport = ts.Client().Transport
			defer func() { http.DefaultTransport = orig }()

			client, err := CreateJiraClient("", "user", "secret", ts.URL)
			if err != nil {
				t.Fatalf("CreateJiraClient: %v", err)
			}
			got, err := IsJiraCloud(client)
			if err != nil {
				t.Fatalf("IsJiraCloud: %v", err)
			}
			if got != tt.want {
				t.Fatalf("IsJiraCloud = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsJiraCloud_CachesDeploymentType(t *testing.T) {
	hits := 0
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deploymentType":"Cloud"}`))
	}))
	defer ts.Close()

	client, err := jira.NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatalf("jira.NewClient: %v", err)
	}
	for i := 0; i < 3; i++ {
		if got, err := IsJiraCloud(client); err != nil || !got {
			t.Fatalf("IsJiraCloud = %v, %v", got, err)
		}
	}
	if hits != 1 {
		t.Fatalf("serverInfo hits = %d, want 1", hits)
	}
}
