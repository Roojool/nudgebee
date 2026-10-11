package events

import (
	"testing"

	"nudgebee/runbook/services/service"

	"github.com/stretchr/testify/assert"
)

// Issue #39585: a recovery may close every open firing of an alert by its
// fingerprint, so finding_id is optional only on a resolve that carries one.
func TestValidateFindingId(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event service.Event
		ok    bool
	}{
		{"firing with finding_id", service.Event{Status: service.EventStatusFiring, FindingId: "f1"}, true},
		{"firing without finding_id", service.Event{Status: service.EventStatusFiring, Fingerprint: "fp"}, false},
		{"resolved by fingerprint", service.Event{Status: service.EventStatusResolved, Fingerprint: "fp"}, true},
		{"closed by fingerprint", service.Event{Status: service.EventStatusClosed, Fingerprint: "fp"}, true},
		{"lowercase resolved by fingerprint", service.Event{Status: "resolved", Fingerprint: "fp"}, true},
		{"resolved with neither id", service.Event{Status: service.EventStatusResolved}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFindingId(tc.event)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// The saved-workflow schema must accept a resolve without finding_id and still
// reject a firing without one.
func TestInputSchemaFindingIdRequiredOnlyWhenFiring(t *testing.T) {
	event := func(status string) map[string]any {
		return map[string]any{
			"title":           "calico-node Unavailable Replicas exceeds 0",
			"description":     "Site24x7 recovery",
			"aggregation_key": "site24x7",
			"finding_type":    "issue",
			"subject_name":    "calico-node",
			"priority":        "HIGH",
			"fingerprint":     "calico-node:unavailable",
			"status":          status,
		}
	}
	schema := (&EventsStoreTask{}).InputSchema()

	assert.NoError(t, schema.Validate(map[string]any{"event": event("RESOLVED")}))
	assert.ErrorContains(t, schema.Validate(map[string]any{"event": event("FIRING")}), "finding_id")
}
