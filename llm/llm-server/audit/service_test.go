package audit

import (
	"testing"
	"time"

	"nudgebee/llm/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func auditWithUserId(userId string) *AuditRequest {
	return &AuditRequest{Audits: []Audit{{
		UserId:        userId,
		EventTime:     time.Now(),
		EventCategory: EventCategoryK8sRelay,
		EventType:     EventTypeK8sRelayTask,
		EventActor:    EventActorK8sAgent,
		EventAction:   EventActionCreate,
		EventTarget:   "kubectl",
		EventStatus:   EventStatusSuccess,
		EventState:    map[string]any{"command": "kubectl get pods"},
	}}}
}

func TestNormalizeAuditUserIds(t *testing.T) {
	realUser := "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

	tests := []struct {
		name   string
		userId string
		want   string
	}{
		{"system sentinel is collapsed", security.GetSystemUserId(), ""},
		{"real uuid4 is preserved", realUser, realUser},
		{"empty stays empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := auditWithUserId(tt.userId)
			normalizeAuditUserIds(req)
			assert.Equal(t, tt.want, req.Audits[0].UserId)
			require.NoError(t, validateAuditRequest(req))
		})
	}
}

// The bug this guards: uuid.Nil is not a v4 uuid, so an un-normalized sentinel
// fails validation and the audit record is dropped.
func TestValidateAuditRequestRejectsUnnormalizedSentinel(t *testing.T) {
	assert.Error(t, validateAuditRequest(auditWithUserId(security.GetSystemUserId())))
}

// The sentinel is collapsed, not waved through: an id that is neither a uuid4
// nor the sentinel must still be rejected.
func TestValidateAuditRequestRejectsMalformedUserId(t *testing.T) {
	req := auditWithUserId("not-a-uuid")
	normalizeAuditUserIds(req)
	assert.Error(t, validateAuditRequest(req))
}

func TestNormalizeAuditUserIdsHandlesNil(t *testing.T) {
	assert.NotPanics(t, func() { normalizeAuditUserIds(nil) })
}
