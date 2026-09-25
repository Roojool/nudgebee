package tools

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nudgebee/tickets-server/models"

	"github.com/gin-gonic/gin"
)

// snStub serves an incident lookup and a PUT, recording how many of each arrive and the PUT body.
type snStub struct {
	mu        sync.Mutex
	gets      int
	puts      int
	putBody   map[string]any
	putStatus int
	delay     time.Duration
}

func (s *snStub) counts() (gets, puts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.puts
}

func newServiceNowStub(t *testing.T, st *snStub) models.TicketConfigurations {
	t.Helper()
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st.delay > 0 {
			time.Sleep(st.delay)
		}
		w.Header().Set("Content-Type", "application/json")
		st.mu.Lock()
		defer st.mu.Unlock()
		if r.Method == http.MethodPut {
			st.puts++
			var reader io.Reader = r.Body
			if r.Header.Get("Content-Encoding") == "gzip" { // kiota compresses request bodies
				if gz, err := gzip.NewReader(r.Body); err == nil {
					reader = gz
				}
			}
			body, _ := io.ReadAll(reader)
			_ = json.Unmarshal(body, &st.putBody)
			if st.putStatus != 0 {
				w.WriteHeader(st.putStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"denied"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"result":{"sys_id":"abc"}}`))
			return
		}
		st.gets++
		_, _ = w.Write([]byte(`{"result":[{"sys_id":"abc"}]}`))
	}))
	t.Cleanup(stub.Close)
	orig := http.DefaultTransport
	http.DefaultTransport = stub.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = orig })
	return models.TicketConfigurations{URL: stub.URL, Username: "u", Password: "p"}
}

func TestServiceNowService_Update_AppliesEveryFieldInOnePut(t *testing.T) {
	st := &snStub{}
	cfg := newServiceNowStub(t, st)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	err := (&ServiceNowService{}).Update(ctx, cfg, "INC0010001", models.UpdateFields{
		Severity: "High", Assignee: "sys-user-1", Description: "**hi**", Status: "In Progress",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	gets, puts := st.counts()
	if gets != 1 || puts != 1 {
		t.Fatalf("want one lookup and one PUT, got %d and %d", gets, puts)
	}
	want := map[string]string{"urgency": "1", "impact": "2", "assigned_to": "sys-user-1", "description": "hi", "state": "2"}
	for k, v := range want {
		if st.putBody[k] != v {
			t.Errorf("PUT %s = %v, want %s (body %v)", k, st.putBody[k], v, st.putBody)
		}
	}
}

func TestServiceNowService_Update_FailureIsOnePutAndAnError(t *testing.T) {
	st := &snStub{putStatus: http.StatusForbidden}
	cfg := newServiceNowStub(t, st)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	err := (&ServiceNowService{}).Update(ctx, cfg, "INC0010001", models.UpdateFields{Severity: "Low", Assignee: "sys-user-1"})
	if err == nil {
		t.Fatal("expected an error when the PUT is refused")
	}
	if _, puts := st.counts(); puts != 1 {
		t.Errorf("a refused update must not be followed by more writes, got %d PUTs", puts)
	}
}

func TestServiceNowService_Update_RejectsSeveralAssigneesBeforeAnyRequest(t *testing.T) {
	st := &snStub{}
	cfg := newServiceNowStub(t, st)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	err := (&ServiceNowService{}).Update(ctx, cfg, "INC0010001", models.UpdateFields{Severity: "High", Assignees: []string{"a", "b"}})
	if err == nil || !strings.Contains(err.Error(), "only a single assignee") {
		t.Fatalf("expected the single-assignee error, got %v", err)
	}
	if gets, puts := st.counts(); gets != 0 || puts != 0 {
		t.Errorf("nothing may be sent when the update is invalid, got %d lookups and %d PUTs", gets, puts)
	}
}

func TestServiceNowService_Update_NothingToChangeSendsNothing(t *testing.T) {
	st := &snStub{}
	cfg := newServiceNowStub(t, st)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	if err := (&ServiceNowService{}).Update(ctx, cfg, "INC0010001", models.UpdateFields{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gets, puts := st.counts(); gets != 0 || puts != 0 {
		t.Errorf("expected no requests, got %d lookups and %d PUTs", gets, puts)
	}
}

func TestServiceNowService_Update_GivesUpOnAnUnresponsiveInstance(t *testing.T) {
	st := &snStub{delay: 1500 * time.Millisecond}
	cfg := newServiceNowStub(t, st)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	orig := servicenowRequestTimeout
	servicenowRequestTimeout = 200 * time.Millisecond
	defer func() { servicenowRequestTimeout = orig }()

	start := time.Now()
	err := (&ServiceNowService{}).Update(ctx, cfg, "INC0010001", models.UpdateFields{Severity: "High"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
		t.Errorf("took %v; the request timeout should have cut it off near 200ms", elapsed)
	}
}

func TestServiceNowStateCode(t *testing.T) {
	for in, want := range map[string]string{
		"new": "1", "Open": "1", "In Progress": "2", "acknowledged": "2", "on_hold": "3",
		"Resolved": "6", "closed": "7", "cancelled": "8", "canceled": "8", "5": "5",
	} {
		if got := serviceNowStateCode(in); got != want {
			t.Errorf("serviceNowStateCode(%q) = %q, want %q", in, got, want)
		}
	}
}
