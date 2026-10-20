package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nudgebee/tickets-server/models"

	jira "github.com/andygrunwald/go-jira"
)

func TestJiraPriority_IDOrNameByShape(t *testing.T) {
	for in, want := range map[string]jira.Priority{
		"3":       {ID: "3"},
		"10001":   {ID: "10001"},
		"High":    {Name: "High"},
		"high":    {Name: "high"},
		" High  ": {Name: "High"},
	} {
		got := jiraPriority(in)
		if got.ID != want.ID || got.Name != want.Name {
			t.Errorf("jiraPriority(%q) = %+v, want %+v", in, *got, want)
		}
	}
}

func TestBuildJiraIssueFields_PriorityNameAndID(t *testing.T) {
	for severity, wantWire := range map[string]string{"High": `"priority":{"name":"High"}`, "3": `"priority":{"id":"3"}`} {
		fields, err := buildJiraIssueFields(models.Ticket{Title: "t", TicketType: "Task", ProjectKey: "KAN", Severity: severity},
			map[string]interface{}{}, noAssignee, acceptParent)
		if err != nil {
			t.Fatal(err)
		}
		blob, _ := json.Marshal(jira.Issue{Fields: fields})
		if !strings.Contains(string(blob), wantWire) {
			t.Errorf("severity %q: payload %s missing %s", severity, blob, wantWire)
		}
	}
}

func TestFieldsForIssueType_MatchesIgnoringCase(t *testing.T) {
	meta := map[string]interface{}{"data": []interface{}{
		map[string]interface{}{"name": "Task", "fields": map[string]interface{}{"summary": map[string]interface{}{"key": "summary"}}},
	}}
	for _, in := range []string{"Task", "task", "TASK", " Task "} {
		if got := FieldsForIssueType(meta, in); got["summary"].Key != "summary" {
			t.Errorf("FieldsForIssueType(%q) found no fields", in)
		}
	}
	if got := FieldsForIssueType(meta, "Epic"); got != nil {
		t.Errorf("unknown type must return nil, got %v", got)
	}
}

func TestJiraPriorityOptions_OnlyWhenPriorityRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"1","name":"Highest"},{"id":"2","name":"High"}]`))
	}))
	defer ts.Close()
	client, err := jira.NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := jiraPriorityOptions(client, &jira.Error{Errors: map[string]string{"priority": "invalid"}}); got != "Highest, High" {
		t.Errorf("options = %q, want %q", got, "Highest, High")
	}
	if got := jiraPriorityOptions(client, &jira.Error{Errors: map[string]string{"summary": "required"}}); got != "" {
		t.Errorf("non-priority rejection must not list priorities, got %q", got)
	}
	if got := jiraPriorityOptions(client, http.ErrHandlerTimeout); got != "" {
		t.Errorf("non-jira error must not list priorities, got %q", got)
	}
}

// jiraStub serves one issue whose current status is currentStatus and whose available
// transitions are the given names; it counts transition POSTs.
func jiraStub(t *testing.T, currentStatus string, transitions []string, posts *int32) *jira.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == http.MethodPost:
			atomic.AddInt32(posts, 1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			items := make([]map[string]interface{}, 0, len(transitions))
			for i, name := range transitions {
				items = append(items, map[string]interface{}{"id": string(rune('1' + i)), "name": name, "to": map[string]string{"name": name}})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"transitions": items})
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"key": "KAN-1", "fields": map[string]interface{}{"status": map[string]string{"name": currentStatus}}})
		}
	}))
	t.Cleanup(ts.Close)
	client, err := jira.NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestTransitionJiraIssue(t *testing.T) {
	t.Run("already in the target status is a no-op", func(t *testing.T) {
		var posts int32
		client := jiraStub(t, "Done", []string{"Reopen"}, &posts)
		if err := transitionJiraIssue(client, "KAN-1", "done"); err != nil {
			t.Fatalf("expected no-op success, got %v", err)
		}
		if posts != 0 {
			t.Errorf("no transition should be posted, got %d", posts)
		}
	})
	t.Run("available transition still runs", func(t *testing.T) {
		var posts int32
		client := jiraStub(t, "To Do", []string{"In Progress", "Done"}, &posts)
		if err := transitionJiraIssue(client, "KAN-1", "Done"); err != nil {
			t.Fatal(err)
		}
		if posts != 1 {
			t.Errorf("expected one transition post, got %d", posts)
		}
	})
	t.Run("unavailable status other than the current one still errors", func(t *testing.T) {
		var posts int32
		client := jiraStub(t, "To Do", []string{"In Progress"}, &posts)
		err := transitionJiraIssue(client, "KAN-1", "Done")
		if err == nil || !strings.Contains(err.Error(), "not available") {
			t.Errorf("expected 'not available' error, got %v", err)
		}
	})
}

func TestResolveJiraPriority_MatchesNameIgnoringCase(t *testing.T) {
	var calls, status int32 = 0, http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(atomic.LoadInt32(&status)))
		_, _ = w.Write([]byte(`[{"id":"2","name":"High"},{"id":"4","name":"Low"}]`))
	}))
	defer ts.Close()
	client, err := jira.NewClient(ts.Client(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	for _, in := range []string{"Low", "LoW", "low", "LOW", " lOw "} {
		if got := resolveJiraPriority(client, &jira.Priority{Name: in}); got.ID != "4" || got.Name != "" {
			t.Errorf("resolve(%q) = %+v, want ID 4", in, *got)
		}
	}
	if got := resolveJiraPriority(client, &jira.Priority{Name: "Urgent"}); got.Name != "Urgent" || got.ID != "" {
		t.Errorf("unknown name must pass through, got %+v", *got)
	}

	if got := resolveJiraPriority(nil, &jira.Priority{Name: "Low"}); got.Name != "Low" || got.ID != "" {
		t.Errorf("a nil client must pass the name through, got %+v", *got)
	}

	before := atomic.LoadInt32(&calls)
	if got := resolveJiraPriority(client, &jira.Priority{ID: "4"}); got.ID != "4" {
		t.Errorf("an ID must pass through, got %+v", *got)
	}
	if got := resolveJiraPriority(client, nil); got != nil {
		t.Errorf("nil must stay nil, got %+v", got)
	}
	if atomic.LoadInt32(&calls) != before {
		t.Error("an ID or nil priority must not call Jira")
	}

	atomic.StoreInt32(&status, http.StatusInternalServerError)
	if got := resolveJiraPriority(client, &jira.Priority{Name: "LoW"}); got.Name != "LoW" || got.ID != "" {
		t.Errorf("a failed lookup must pass the name through, got %+v", *got)
	}
}
