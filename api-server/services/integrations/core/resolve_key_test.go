package core

import (
	"testing"

	"nudgebee/services/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// A resolve closes the events of its own fingerprint and then, when the parser
// reports one, those stored under the fingerprint the alert carried before its
// format changed. Without the second close, a Datadog group event opened under the
// monitor-wide "<monitorID>-" key would never match its group's recovery again.
func TestResolveEventAlsoClosesLegacyFingerprint(t *testing.T) {
	mock := mockMetastoreForRecovery(t)
	const tenant, account = "7a1d0c3e-0000-4000-8000-000000000001", "7a1d0c3e-0000-4000-8000-0000000000a1"
	const groupKey, legacyKey = "8249055-issue.id:655bd304-5322-11f0-80d4-da7ad0900000", "8249055-"

	// prepareQueryForIn renumbers placeholders in textual order: $4, $5, $1, $2, $3, $3, $4.
	for _, key := range []string{groupKey, legacyKey} {
		mock.ExpectQuery(`update events set status`).
			WithArgs("CLOSED", sqlmock.AnyArg(), tenant, account, key, key, "CLOSED").
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
	}

	e := EventIncomingWebhook{
		EventId:           "8169025783718885557",
		EventStatus:       "RESOLVED",
		Investigation:     EventIncomingWebhookInvestigation{Fingerprint: groupKey},
		LegacyFingerprint: legacyKey,
	}
	require.NoError(t, resolveEvent(security.NewRequestContextForSuperAdmin(nil, nil, nil), tenant, account, "datadog_webhook", e))
	assert.NoError(t, mock.ExpectationsWereMet(), "the recovery must close under its group key and under the legacy key")
}
