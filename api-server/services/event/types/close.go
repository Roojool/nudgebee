package types

import "time"

// The statements below are every way an incoming resolve closes an event. They
// live here rather than beside their callers because the callers cannot import
// each other: integrations/core reaches event.InvestigateEvent only through a
// registered callback, and both packages already import this one.
//
// All of them share the same close semantics: status becomes CLOSED, ends_at is
// stamped in the same statement, and a row that is already CLOSED is left alone
// so RETURNING id lists only genuine open→closed transitions. Callers publish one
// resolved notification per returned id, so that guard is what keeps a repeated
// resolve from re-notifying.
//
// ends_at is written alongside status. Closing without it leaves a row that is
// CLOSED but open-ended, and every consumer that measures how long an alert ran
// counts only `ends_at IS NOT NULL AND ends_at > starts_at` — so such rows read
// as "never resolved". triage.computeAlertQuality then scores the rule
// resolution_rate = 0 and classifies it "broken", which is how a healthy
// alert ends up recommended for manual review. COALESCE keeps any end time the
// ingest path already recorded.

// ResolveEventQuery closes the event a webhook resolve delivery refers to.
//
// It matches finding_id as well as fingerprint. convertWebhookEventToEvent writes
// FindingId from EventId, so the triggering delivery always leaves the event
// findable by the id its own resolve delivery carries — whereas fingerprint is
// whatever the producing integration chose. PagerDuty overwrites
// Investigation.Fingerprint with the CEF dedup_key, which is stable across every
// incident for the same alert and so never equals the incident id a resolve
// carries: the fingerprint predicate alone matched nothing and PagerDuty resolves
// closed no events at all. That went unnoticed because the k8s-collector's
// resource reconciliation bulk-closes events later, so they did eventually clear.
//
// Tenant-scoped so the finding_id predicate can use events_cloudaccount_findingid,
// whose leading column is tenant. Verified on a real dataset: both predicates
// resolve through a BitmapOr of index scans, no sequential scan.
const ResolveEventQuery = `update events set status = $4, ends_at = COALESCE(ends_at, $5) where tenant = $1 and cloud_account_id = $2 and (fingerprint = $3 or finding_id = $3) and status != $4 returning id`

// CloseEventByIdQuery closes the one event a trigger_investigation resolve names by
// finding_id (issue #39585). InvestigateEvent has already looked the row up by
// (tenant, cloud_account_id, finding_id), so it closes by primary key.
//
// source is part of the predicate because finding_id is chosen by the sender: a
// RESOLVED from an automation must not close an Alertmanager or Zenduty event
// that happens to carry the same id.
const CloseEventByIdQuery = `update events set status = $2, ends_at = COALESCE(ends_at, $3) where id = $1 and source = $4 and status != $2 returning id`

// CloseEventsByFingerprintQuery closes every open event of one alert identity in an
// account, for a trigger_investigation resolve that does not know which firing it
// ends (no finding_id, or one that matches no row). fingerprint is the stable
// identity every firing of the alert carries; finding_id is one firing.
//
// Scoped to the same source for the same reason as CloseEventByIdQuery: the
// fingerprint is sender-chosen, and another integration's events sharing it are
// not this sender's to close.
const CloseEventsByFingerprintQuery = `update events set status = $4, ends_at = COALESCE(ends_at, $5) where tenant = $1 and cloud_account_id = $2 and fingerprint = $3 and source = $6 and status != $4 returning id`

// ResolveEndsAt is the end time to stamp on an event a resolve closes: the end
// time the sender reported, or the delivery time when it reported none
// (PagerDuty's incident.resolved, ServiceNow, and the generic webhook all leave
// it zero).
func ResolveEndsAt(reported time.Time) time.Time {
	if !reported.IsZero() {
		return reported
	}
	return time.Now().UTC()
}
