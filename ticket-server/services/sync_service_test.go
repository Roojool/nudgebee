package services

import (
	"errors"
	"testing"

	"nudgebee/tickets-server/models"
	ticketmgr "nudgebee/tickets-server/services/ticket"

	"github.com/gin-gonic/gin"
)

// stubTicketManager answers Get from a canned map keyed by ticket ID, or an
// error for IDs not present (or explicitly listed in errs).
type stubTicketManager struct {
	ticketmgr.TicketManager
	byID map[string]*models.Ticket
	errs map[string]error
	gets []string // ticket IDs Get was called with, in order
}

func (m *stubTicketManager) Get(_ *gin.Context, _ models.TicketConfigurations, ticketID string) (*models.Ticket, error) {
	m.gets = append(m.gets, ticketID)
	if err, ok := m.errs[ticketID]; ok {
		return nil, err
	}
	if t, ok := m.byID[ticketID]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}

// A ServiceNow (or any non-Jira) ticket resolved upstream must produce an
// update once synced — this is the nudgebee-enterprise#39092 bug: SyncTickets
// used to hard-skip every tool but Jira, so a ServiceNow incident closed
// upstream stayed "open" in Nudgebee forever.
func TestCollectManagerUpdates_NonJiraToolProducesUpdate(t *testing.T) {
	stored := []models.Ticket{
		{ID: "row-1", TicketID: "INC001", Status: "open"},
	}
	manager := &stubTicketManager{byID: map[string]*models.Ticket{
		"INC001": {TicketID: "INC001", Status: "Resolved"},
	}}

	updates := collectManagerUpdates(newSafeGinContext(), manager, models.TicketConfigurations{Tool: "servicenow"}, stored)

	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(updates))
	}
	if updates[0].rowID != "row-1" || updates[0].fields.Status != "Resolved" {
		t.Fatalf("update = %+v, want rowID=row-1 status=Resolved", updates[0])
	}
}

func TestCollectManagerUpdates_NoChangeProducesNoUpdate(t *testing.T) {
	stored := []models.Ticket{{ID: "row-1", TicketID: "INC001", Status: "open"}}
	manager := &stubTicketManager{byID: map[string]*models.Ticket{
		"INC001": {TicketID: "INC001", Status: "open"},
	}}

	updates := collectManagerUpdates(newSafeGinContext(), manager, models.TicketConfigurations{Tool: "servicenow"}, stored)
	if len(updates) != 0 {
		t.Fatalf("got %d updates, want 0", len(updates))
	}
}

// A field the platform reports blank (unassigned, no severity concept) must
// not clear a previously recorded value — only a platform-reported value
// overwrites what's stored.
func TestDiffTicketUpdate_BlankFieldDoesNotClearStoredValue(t *testing.T) {
	stored := models.Ticket{Status: "open", Severity: "P2", Assignee: "jdoe"}
	current := models.Ticket{Status: "closed"} // severity/assignee left blank, as GitHub/GitLab always report

	fields, update := diffTicketUpdate(stored, current)
	if !update {
		t.Fatal("expected an update for the changed status")
	}
	if fields.Status != "closed" {
		t.Errorf("Status = %q, want closed", fields.Status)
	}
	if fields.Severity != "" {
		t.Errorf("Severity = %q, want empty (blank platform field must not clear stored value)", fields.Severity)
	}
	if fields.Assignee != "" {
		t.Errorf("Assignee = %q, want empty (blank platform field must not clear stored value)", fields.Assignee)
	}
}

// consecutiveFetchFailureLimit consecutive errors abort the remaining tickets
// for that configuration's run, but a success in between resets the streak.
func TestCollectManagerUpdates_AbortsAfterConsecutiveFailures(t *testing.T) {
	stored := []models.Ticket{
		{ID: "row-1", TicketID: "T1", Status: "open"},
		{ID: "row-2", TicketID: "T2", Status: "open"},
		{ID: "row-3", TicketID: "T3", Status: "open"},
		{ID: "row-4", TicketID: "T4", Status: "open"}, // never reached
	}
	manager := &stubTicketManager{
		byID: map[string]*models.Ticket{},
		errs: map[string]error{
			"T1": errors.New("boom"),
			"T2": errors.New("boom"),
			"T3": errors.New("boom"),
		},
	}

	collectManagerUpdates(newSafeGinContext(), manager, models.TicketConfigurations{Tool: "github"}, stored)

	if len(manager.gets) != 3 {
		t.Fatalf("Get was called %d times, want 3 (abort before T4)", len(manager.gets))
	}
}

func TestCollectManagerUpdates_SuccessResetsFailureStreak(t *testing.T) {
	stored := []models.Ticket{
		{ID: "row-1", TicketID: "T1", Status: "open"},
		{ID: "row-2", TicketID: "T2", Status: "open"},
		{ID: "row-3", TicketID: "T3", Status: "open"},
		{ID: "row-4", TicketID: "T4", Status: "open"},
	}
	manager := &stubTicketManager{
		byID: map[string]*models.Ticket{
			"T2": {TicketID: "T2", Status: "open"}, // success, resets the streak
			"T4": {TicketID: "T4", Status: "closed"},
		},
		errs: map[string]error{
			"T1": errors.New("boom"),
			"T3": errors.New("boom"),
		},
	}

	updates := collectManagerUpdates(newSafeGinContext(), manager, models.TicketConfigurations{Tool: "github"}, stored)

	if len(manager.gets) != 4 {
		t.Fatalf("Get was called %d times, want 4 (no abort, since no 3 consecutive failures)", len(manager.gets))
	}
	if len(updates) != 1 || updates[0].rowID != "row-4" {
		t.Fatalf("updates = %+v, want a single update for row-4", updates)
	}
}

// Jira issue keys are case-insensitive; the batch sync path matches a search
// result back to its stored ticket by key, so a case mismatch there would
// silently skip that ticket's update.
func TestNormalizeJiraKey(t *testing.T) {
	cases := map[string]string{
		"PROJ-123":  "PROJ-123",
		"proj-123":  "PROJ-123",
		" PROJ-123": "PROJ-123",
		"Proj-123 ": "PROJ-123",
	}
	for in, want := range cases {
		if got := normalizeJiraKey(in); got != want {
			t.Errorf("normalizeJiraKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A TicketManager.Get implementation receives the concrete *gin.Context, not
// just the context.Context interface, so it could call a gin-specific method
// like GetHeader or ClientIP that dereferences Request directly — those would
// nil-panic the background sync goroutine on a zero-value &gin.Context{}.
func TestNewSafeGinContext_HasNonNilRequest(t *testing.T) {
	ctx := newSafeGinContext()
	if ctx.Request == nil {
		t.Fatal("Request is nil; a gin.Context method that reads it directly would panic")
	}
}
