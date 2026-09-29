package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A GCP resolve closes the incident it names. Its fingerprint follows polling's
// policy:type:resource format, which changed for webhook rows (#34377); the incident
// id did not, so it still matches rows stored before that change.
func TestResolveKey(t *testing.T) {
	e := EventIncomingWebhook{
		EventId:       "projects/example-project/alerts/0.abc",
		Investigation: EventIncomingWebhookInvestigation{Fingerprint: "projects/example-project/alertPolicies/1:cloudsql_database:example-project:sample-db"},
	}
	assert.Equal(t, e.EventId, resolveKey("gcp_monitoring_webhook", e))
	assert.Equal(t, e.EventId, resolveKey("pagerduty_webhook", e))
	assert.Equal(t, e.Investigation.Fingerprint, resolveKey("datadog_webhook", e))
}
