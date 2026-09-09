package event

import (
	"errors"
	"fmt"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/internal/database"
	"nudgebee/services/internal/database/models"
	"nudgebee/services/security"

	"github.com/lib/pq"
)

// Enrichment outcomes, reported on the enrichment duration metric.
const (
	EnrichmentOutcomeSuccess = "success"
	EnrichmentOutcomePartial = "partial"
	EnrichmentOutcomeError   = "error"
)

// EnrichEvent runs the playbook for an already-persisted event and merges the
// results into its row. It is the asynchronous half of InvestigateEvent: the
// synchronous half committed the row and returned the id to the caller.
//
// The returned outcome is one of the EnrichmentOutcome constants. A non-nil error
// means the row could not be loaded or written — the playbook failing is normal
// and reported as "partial"/"error" without stopping the write, because an event
// with incomplete evidence still has to reach triage.
func EnrichEvent(sc *security.RequestContext, eventId string, deadline time.Time) (string, error) {
	eventObj, err := GetEvent(sc, eventId)
	if err != nil {
		return EnrichmentOutcomeError, fmt.Errorf("event: failed to load event for enrichment: %w", err)
	}

	webhookEvent, err := eventForEnrichment(eventObj)
	if err != nil {
		return EnrichmentOutcomeError, err
	}

	// Skip playbook execution for configuration change events, matching the inline
	// path. These should not normally be queued at all — InvestigateEvent never
	// defers them — so this is a guard against a stale message, not a live case.
	if webhookEvent.FindingType == "configuration_change" {
		sc.GetLogger().Info("event: skipping enrichment for configuration change event", "event_id", eventId)
		return EnrichmentOutcomeSuccess, nil
	}

	evidenceCountBefore := len(webhookEvent.Evidences)
	addedEvidences, labelDelta := runPlaybookEnrichment(sc, &webhookEvent, eventId, deadline)

	outcome := EnrichmentOutcomeSuccess
	if len(addedEvidences) == 0 && evidenceCountBefore == len(webhookEvent.Evidences) {
		// Nothing came back. Either the playbook is empty for this alert or every
		// action failed; both are reported as partial so the metric separates them
		// from runs that produced evidence.
		outcome = EnrichmentOutcomePartial
	}

	if err := applyEnrichment(sc, eventId, &webhookEvent, addedEvidences, labelDelta); err != nil {
		return EnrichmentOutcomeError, err
	}

	return outcome, nil
}

// eventForEnrichment projects the persisted row onto the Event shape the playbook
// path expects. Only the fields the playbook and its post-processing read are
// carried across — this is not a general-purpose converter.
//
// Every dereference is guarded: subject_name and friends are nullable columns,
// and this runs on a queue consumer where a panic would take the process down.
func eventForEnrichment(eventObj models.Event) (Event, error) {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}

	accountId := deref(eventObj.CloudAccountId)
	if accountId == "" {
		return Event{}, errors.New("event: cloud_account_id is not available on the event")
	}
	if deref(eventObj.Tenant) == "" {
		return Event{}, errors.New("event: tenant is not available on the event")
	}

	labels := map[string]string{}
	if eventObj.Labels != nil && eventObj.Labels.IsObject() {
		if obj, ok := eventObj.Labels.Object().(map[string]any); ok {
			for k, v := range obj {
				// Labels is a jsonb column: a non-string value is possible and must
				// not panic the consumer. Skipping is right — the playbook and the
				// label templates only ever read strings.
				if s, ok := v.(string); ok {
					labels[k] = s
				}
			}
		}
	}

	var evidences []any
	if eventObj.Evidences != nil && eventObj.Evidences.IsArray() {
		evidences = eventObj.Evidences.Array()
	}

	return Event{
		AccountId:        accountId,
		Tenant:           deref(eventObj.Tenant),
		Source:           deref(eventObj.Source),
		AggregationKey:   deref(eventObj.AggregationKey),
		FindingType:      deref(eventObj.FindingType),
		SubjectType:      deref(eventObj.SubjectType),
		SubjectName:      deref(eventObj.SubjectName),
		SubjectNamespace: deref(eventObj.SubjectNamespace),
		SubjectNode:      deref(eventObj.SubjectNode),
		SubjectOwner:     deref(eventObj.SubjectOwner),
		SubjectOwnerKind: deref(eventObj.SubjectOwnerKind),
		Cluster:          deref(eventObj.Cluster),
		CloudResourceId:  deref(eventObj.CloudResourceId),
		Fingerprint:      deref(eventObj.Fingerprint),
		FindingId:        deref(eventObj.FindingId),
		StartsAt:         eventObj.StartsAt,
		EndsAt:           eventObj.EndsAt,
		Labels:           labels,
		Evidences:        evidences,
	}, nil
}

// applyEnrichment merges one enrichment run into the event row.
//
// It is a single statement on purpose. Four other writers touch this row while
// enrichment is in flight — a workflow's events.add_evidence, the duplicate-event
// branch of InvestigateEvent, cloud-collector resolving the event, and
// RefreshInvestigation — so a load-modify-write would silently drop whichever of
// them landed in between. Evaluating the filter and the append against one row
// version under one row lock is what makes that impossible.
//
// It is also idempotent: the filter strips this run's own previous output before
// re-appending, so a redelivered message cannot stack duplicate cards. Cards from
// any other producer (the collector's inline get_resource / Raw Event, a
// workflow's) are matched by neither and survive.
func applyEnrichment(sc *security.RequestContext, eventId string, webhookEvent *Event, addedEvidences []any, labelDelta map[string]string) error {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return fmt.Errorf("event: unable to get dbms for enrichment: %w", err)
	}

	if addedEvidences == nil {
		addedEvidences = []any{}
	}
	evidencesJson, err := common.MarshalJson(addedEvidences)
	if err != nil {
		return fmt.Errorf("event: failed to marshal enrichment evidences: %w", err)
	}

	if labelDelta == nil {
		labelDelta = map[string]string{}
	}
	labelsJson, err := common.MarshalJson(labelDelta)
	if err != nil {
		return fmt.Errorf("event: failed to marshal enrichment labels: %w", err)
	}

	// Action names this run produced, used to strip its own prior output on a
	// redelivery. Two ways this list can destroy the column, both guarded:
	//   - an empty string in it matches every card lacking an action_name, since
	//     the SQL compares against coalesce(..., ''). regeneratedActionNames drops
	//     empty names, and a test asserts it.
	//   - a NULL array makes `<> ALL(...)` evaluate to NULL for every row, so the
	//     WHERE keeps nothing and the whole column is replaced by this run's cards.
	//     This slice is always non-nil, so that is unreachable today; the SQL
	//     coalesces the parameter anyway, because the failure is silent and total.
	regenerated := regeneratedActionNames(addedEvidences)

	res, err := dbms.Db.Exec(
		`UPDATE events
		    SET evidences = (
		            SELECT coalesce(jsonb_agg(elem), '[]'::jsonb)
		              FROM jsonb_array_elements(
		                     CASE WHEN jsonb_typeof(evidences) = 'array'
		                          THEN evidences ELSE '[]'::jsonb
		                     END) AS elem
		             WHERE coalesce(elem->'additional_info'->>'action_name', '') <> ALL(coalesce($1::text[], '{}'::text[]))
		               AND coalesce(elem->'additional_info'->>'actual_action_name', '') <> ALL(coalesce($1::text[], '{}'::text[]))
		        ) || $2::jsonb,
		        labels = CASE
		                WHEN jsonb_typeof(labels) = 'object' THEN labels
		                ELSE '{}'::jsonb
		            END || $3::jsonb,
		        cloud_resource_id = COALESCE(cloud_resource_id, NULLIF($4, '')::uuid),
		        cluster           = COALESCE(NULLIF(cluster, ''), NULLIF($5, '')),
		        subject_namespace = COALESCE(NULLIF(subject_namespace, ''), NULLIF($6, '')),
		        subject_name      = COALESCE(NULLIF(subject_name, ''), NULLIF($7, '')),
		        updated_at        = $8
		  WHERE id = $9`,
		pq.Array(regenerated),
		string(evidencesJson),
		string(labelsJson),
		webhookEvent.CloudResourceId,
		webhookEvent.Cluster,
		webhookEvent.SubjectNamespace,
		webhookEvent.SubjectName,
		time.Now().UTC(),
		eventId,
	)
	if err != nil {
		return fmt.Errorf("event: failed to apply enrichment: %w", err)
	}
	if affected, aerr := res.RowsAffected(); aerr == nil && affected == 0 {
		return fmt.Errorf("event %s not found", eventId)
	}

	sc.GetLogger().Info("event: applied enrichment",
		"event_id", eventId,
		"evidences_added", len(addedEvidences),
		"labels_changed", len(labelDelta))
	return nil
}

// regeneratedActionNames lists the action names present in this run's evidence,
// so a redelivery replaces them rather than appending a second copy. Empty names
// are dropped — see the caller for why that matters.
func regeneratedActionNames(evidences []any) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, e := range evidences {
		card, ok := e.(map[string]any)
		if !ok {
			continue
		}
		info, ok := card["additional_info"].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"action_name", "actual_action_name"} {
			name, ok := info[key].(string)
			if !ok || name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
