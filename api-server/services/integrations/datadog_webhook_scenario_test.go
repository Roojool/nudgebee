package integrations_test

// Lifecycle of Datadog monitor deliveries against a real Postgres, on the harness
// in event_webhook_scenario_test.go: the token lookup, the Datadog parser, routing
// and resolveEventQuery are all real. InvestigateEvent's stand-in inserts through
// the real event.InsertEvent, so its point-in-time rule decides the stored status
// exactly as in production. Skipped unless NB_TEST_PG_DSN is set (see that file).

import (
	"fmt"
	"net/url"
	"testing"

	"nudgebee/services/config"
	"nudgebee/services/event"
	eventtypes "nudgebee/services/event/types"
	"nudgebee/services/integrations/core"
	"nudgebee/services/security"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ddIntegration = "7a1d0c3e-0000-4000-8000-0000000000d1"
	ddToken       = "datadog-scenario-token"
	ddPath        = "/api/webhooks/datadog"
	ddMonitor     = "8249055"
)

func ddSetup(t *testing.T) (*sqlx.DB, *security.RequestContext) {
	t.Helper()
	db, sc := caSetup(t)
	_, err := db.Exec(`
insert into integrations values ('` + ddIntegration + `', '` + caTenant + `', 'datadog_webhook', 'datadog', 'enabled');
insert into integrations_cloud_accounts values ('` + ddIntegration + `', '` + caAccount + `');
insert into integration_config_values(integration_id, name, value) values ('` + ddIntegration + `', 'token', '` + ddToken + `');
alter table events
  add column title text, add column description text, add column aggregation_key text, add column priority text,
  add column subject_type text, add column subject_namespace text, add column subject_node text, add column service_key text,
  add column evidences jsonb, add column finding_type text, add column cluster text, add column labels jsonb,
  add column updated_at timestamp, add column cloud_resource_id text, add column category text, add column created_at timestamp,
  add column urgency text, add column subject_owner text, add column subject_owner_kind text, add column nb_status text,
  add column principal text;`)
	require.NoError(t, err)

	prevPointInTime, prevFn := config.Config.FeatureEventPointInTimeCloseEnabled, core.InvestigateEventFn
	config.Config.FeatureEventPointInTimeCloseEnabled = true // the production default
	// InvestigateEvent only inserts a finding_id it has not seen; the insert itself
	// is the real one.
	core.InvestigateEventFn = func(_ *security.RequestContext, e eventtypes.Event, _ string) (string, error) {
		var seen int
		if err := db.Get(&seen, `select count(*) from events where tenant = $1 and cloud_account_id = $2 and finding_id = $3`, e.Tenant, e.AccountId, e.FindingId); err != nil || seen > 0 {
			return "", err
		}
		return event.InsertEvent(e, "", event.WithDeferredPostProcess(func(string) {}), event.WithoutWorkflowRefire())
	}
	t.Cleanup(func() {
		config.Config.FeatureEventPointInTimeCloseEnabled = prevPointInTime
		core.InvestigateEventFn = prevFn
	})
	return db, sc
}

// ddDelivery is a monitor notification in the shape of the captured
// error-tracking deliveries in datadog_webhook_test.go: a template without
// $AGGREG_KEY, the group only in the title and the monitor-status link, and
// last_updated equal to date as Datadog sends it.
func ddDelivery(id, transition, issue string, ms int64) string {
	group := url.QueryEscape("issue.id:" + issue)
	return fmt.Sprintf(`{"id":"%s","last_updated":"%d","event_type":"error_tracking_alert",
		"title":"[P2] [%s on {issue.id:%s}] [New issue] New issue to review","date":"%d",
		"org":{"id":"1300568798","name":"Nudgebee"},
		"body":"%%%%%%\nA new issue has been detected.\n\n- - -\n\n[[Monitor Status](https://us5.datadoghq.com/monitors/%s?group=%s&from_ts=%d&to_ts=%d&event_id=%s&link_source=monitor_notif)] · [[Edit Monitor](https://us5.datadoghq.com/monitors/%s/edit?link_source=monitor_notif)]\n%%%%%%"}`,
		id, ms, transition, issue, ms, ddMonitor, group, ms-900000, ms+300000, id, ddMonitor)
}

type ddEvent struct {
	FindingId   string `db:"finding_id"`
	Fingerprint string `db:"fingerprint"`
	Status      string `db:"status"`
	NbStatus    string `db:"nb_status"`
	Ended       bool   `db:"ended"`
}

func ddEvents(t *testing.T, db *sqlx.DB) map[string]ddEvent {
	t.Helper()
	var rows []ddEvent
	require.NoError(t, db.Select(&rows, `select finding_id, coalesce(fingerprint, '') as fingerprint, status,
		coalesce(nb_status, '') as nb_status, ends_at is not null as ended from events`))
	byId := make(map[string]ddEvent, len(rows))
	for _, r := range rows {
		byId[r.FindingId] = r
	}
	return byId
}

func ddDeliver(t *testing.T, sc *security.RequestContext, payload string) {
	t.Helper()
	_, err := deliver(t, sc, ddPath, ddToken, payload)
	require.NoError(t, err)
}

// A [Triggered] delivery is an alert that is firing now. With last_updated passed
// on as ends_at it arrived as a zero-length interval, and InsertEvent stored it
// CLOSED / RESOLVED on arrival.
func TestScenarioDatadogFiringIsStoredOpen(t *testing.T) {
	db, sc := ddSetup(t)
	ddDeliver(t, sc, ddDelivery("1001", "Triggered", "aaaa", 1751802855000))

	ev := ddEvents(t, db)["1001"]
	assert.Equal(t, string(eventtypes.EventStatusFiring), ev.Status)
	assert.Equal(t, "OPEN", ev.NbStatus)
	assert.False(t, ev.Ended, "a firing alert has no end yet")
}

// Two groups of one monitor fire; the recovery of one closes that group's
// events (both the first notification and its re-notification) and leaves the
// other group open until its own recovery. A retried delivery adds no row.
func TestScenarioDatadogRecoveryClosesOnlyItsGroup(t *testing.T) {
	db, sc := ddSetup(t)
	ddDeliver(t, sc, ddDelivery("1001", "Triggered", "aaaa", 1751802855000))
	ddDeliver(t, sc, ddDelivery("1002", "Triggered", "bbbb", 1751802860000))
	ddDeliver(t, sc, ddDelivery("1003", "Re-Triggered", "aaaa", 1751806455000))
	ddDeliver(t, sc, ddDelivery("1003", "Re-Triggered", "aaaa", 1751806455000)) // Datadog retry

	ev := ddEvents(t, db)
	require.Len(t, ev, 3, "a retried delivery must not add a row")
	assert.Equal(t, ev["1001"].Fingerprint, ev["1003"].Fingerprint, "one group keeps one fingerprint")
	assert.NotEqual(t, ev["1001"].Fingerprint, ev["1002"].Fingerprint, "each group has its own fingerprint")
	for _, id := range []string{"1001", "1002", "1003"} {
		assert.Equal(t, string(eventtypes.EventStatusFiring), ev[id].Status, id)
	}

	ddDeliver(t, sc, ddDelivery("1004", "Recovered", "aaaa", 1751810055000))
	ev = ddEvents(t, db)
	require.Len(t, ev, 3, "a recovery closes events, it does not add one")
	for _, id := range []string{"1001", "1003"} {
		assert.Equal(t, "CLOSED", ev[id].Status, id)
		assert.True(t, ev[id].Ended, id)
	}
	assert.Equal(t, string(eventtypes.EventStatusFiring), ev["1002"].Status, "another group's recovery must leave this one open")

	ddDeliver(t, sc, ddDelivery("1005", "Recovered", "bbbb", 1751813655000))
	assert.Equal(t, "CLOSED", ddEvents(t, db)["1002"].Status)
}

// Events opened before this change carry the monitor-wide fingerprint
// "<monitorID>-". A recovery now arrives under its group's fingerprint, and must
// still close them, or they would stay open for good — also when the group has
// re-notified since, so the recovery does match a new-style event as well.
func TestScenarioDatadogRecoveryClosesLegacyFingerprintEvent(t *testing.T) {
	db, sc := ddSetup(t)
	_, err := db.Exec(`insert into events(tenant, cloud_account_id, finding_id, fingerprint, source, status, starts_at)
		values ($1, $2, '900', $3, 'datadog_webhook', 'FIRING', '2025-07-06 11:54:15')`, caTenant, caAccount, ddMonitor+"-")
	require.NoError(t, err)
	ddDeliver(t, sc, ddDelivery("1003", "Re-Triggered", "aaaa", 1751806455000))

	ddDeliver(t, sc, ddDelivery("1004", "Recovered", "aaaa", 1751810055000))

	ev := ddEvents(t, db)
	for _, id := range []string{"900", "1003"} {
		assert.Equal(t, "CLOSED", ev[id].Status, id)
		assert.True(t, ev[id].Ended, id)
	}
}
