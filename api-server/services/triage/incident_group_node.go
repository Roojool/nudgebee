package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"nudgebee/services/internal/database/models"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// incident_group_node.go is the node axis of incident grouping: when a Kubernetes
// node runs out of memory, the pods it kills and the node's own MemoryPressure
// are one incident, even across namespaces and with no service-map edge between
// them. Without this axis they share no subject and no edge, so every kill on a
// full node became an incident of its own.
//
// Membership needs evidence, not just co-timing. An OOM kill joins only when the
// noisy-neighbours card recorded that the pod died below its own limit on a node
// that had run out (oom_cause = node_memory). A node_pressure event joins only
// when the condition that fired is MemoryPressure. Two pods on one node that each
// outgrew their own limit are two problems. DiskPressure kills nothing for
// memory. A plain "same node" rule was considered and rejected: most alerts that
// share a node within minutes are image-pull failures fanning out from one
// registry, where the node is only where the pod happened to land.

const (
	nodeGroupingEnvFlag        = "INCIDENT_NODE_GROUPING"
	oomKillAggregationKey      = "pod_oom_killer_enricher"
	nodePressureAggregationKey = "node_pressure"
	// oomCauseNodeMemory is the noisy-neighbours card's verdict for a pod killed
	// because its node ran out (observability.oomCause).
	oomCauseNodeMemory = "node_memory"
	// nodeMemoryReasonPrefix starts every link this path writes, so its links can
	// be counted, audited, or removed as one set.
	nodeMemoryReasonPrefix = "node memory:"
)

// nodeGroupingEnabled defaults to true; only an explicit "false"/"0" disables,
// matching the other grouping switches.
func nodeGroupingEnabled() bool {
	v := strings.TrimSpace(os.Getenv(nodeGroupingEnvFlag))
	return !strings.EqualFold(v, "false") && v != "0"
}

// nodeFailure is the part of an event the node axis reads.
type nodeFailure struct {
	AggregationKey string
	SubjectType    string
	SubjectName    string
	SubjectNode    string
	Evidences      *models.Json
}

// nodeMemoryFailure reports which node an event says ran out of memory, and
// whether the event is about the node itself rather than a pod on it. ok is false
// for everything else, including an OOM kill the card could not attribute to the
// node.
//
// Node names are lower-cased so a candidate and the seed compare equal however
// the producer cased them.
func nodeMemoryFailure(f nodeFailure) (node string, isNode bool, ok bool) {
	switch f.AggregationKey {
	case oomKillAggregationKey:
		node = strings.ToLower(strings.TrimSpace(f.SubjectNode))
		if node == "" || oomCauseFromEvidence(f.Evidences) != oomCauseNodeMemory {
			return "", false, false
		}
		return node, false, true
	case nodePressureAggregationKey:
		// The agent's node condition events name the node as the subject and leave
		// subject_node empty; take the column when a producer fills it.
		node = strings.TrimSpace(f.SubjectNode)
		if node == "" && strings.EqualFold(strings.TrimSpace(f.SubjectType), "node") {
			node = strings.TrimSpace(f.SubjectName)
		}
		node = strings.ToLower(node)
		if node == "" || !memoryPressureFromEvidence(f.Evidences) {
			return "", false, false
		}
		return node, true, true
	}
	return "", false, false
}

// NodeMemoryNode is nodeMemoryFailure for the read side: the node an event says
// ran out of memory, or "" when the event is not such a failure. One rule decides
// both what incident grouping links and what the incident assembly shows.
func NodeMemoryNode(aggregationKey, subjectType, subjectName, subjectNode string, evidences *models.Json) string {
	node, _, _ := nodeMemoryFailure(nodeFailure{
		AggregationKey: aggregationKey,
		SubjectType:    subjectType,
		SubjectName:    subjectName,
		SubjectNode:    subjectNode,
		Evidences:      evidences,
	})
	return node
}

// NodeMemoryAggregationKeys are the alert types that can be a node memory
// failure, for a caller that needs to query for them.
func NodeMemoryAggregationKeys() []string {
	return []string{oomKillAggregationKey, nodePressureAggregationKey}
}

// evidenceCards returns the evidence array as maps, skipping anything else.
func evidenceCards(ev *models.Json) []map[string]any {
	if ev == nil || !ev.IsArray() {
		return nil
	}
	cards := make([]map[string]any, 0, len(ev.Array()))
	for _, item := range ev.Array() {
		if card, ok := item.(map[string]any); ok {
			cards = append(cards, card)
		}
	}
	return cards
}

// oomCauseFromEvidence reads the oom_cause the noisy-neighbours card recorded, or
// "" when the event has no such card or the card predates the field. The last
// card wins: a refreshed investigation appends a newer one.
func oomCauseFromEvidence(ev *models.Json) string {
	cause := ""
	for _, card := range evidenceCards(ev) {
		if t, _ := card["type"].(string); t != "json" {
			continue
		}
		info, _ := card["additional_info"].(map[string]any)
		if action, _ := info["action_name"].(string); action != "noisy_neighbours_enricher" {
			continue
		}
		data, _ := card["data"].(string)
		var payload struct {
			Data struct {
				OOMCause string `json:"oom_cause"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(data), &payload) == nil && payload.Data.OOMCause != "" {
			cause = payload.Data.OOMCause
		}
	}
	return cause
}

// memoryPressureFromEvidence reports whether the node snapshot that raised a
// node_pressure event has MemoryPressure True. The event's fingerprint is shared
// by every pressure condition on the node, so the condition has to be read from
// the snapshot itself.
func memoryPressureFromEvidence(ev *models.Json) bool {
	for _, card := range evidenceCards(ev) {
		if t, _ := card["type"].(string); t != "json" {
			continue
		}
		data, _ := card["data"].(string)
		if !strings.Contains(data, "MemoryPressure") {
			continue // skip parsing cards that cannot answer
		}
		var snapshot struct {
			Kind string `json:"kind"`
			Obj  struct {
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			} `json:"obj"`
		}
		if json.Unmarshal([]byte(data), &snapshot) != nil || !strings.EqualFold(snapshot.Kind, "Node") {
			continue
		}
		for _, c := range snapshot.Obj.Status.Conditions {
			if c.Type == "MemoryPressure" && strings.EqualFold(c.Status, "True") {
				return true
			}
		}
	}
	return false
}

// nodeCandidate is one alert on the seed's node, as the node pool decision needs it.
type nodeCandidate struct {
	candidate  groupCandidate
	subjectKey string
	isNode     bool
	// leaderAllowed is false when the alert already belongs to a group whose
	// leader is not a memory failure on this node.
	leaderAllowed bool
}

// poolNodeMembers picks the candidates that belong in the seed's node incident.
// Pure — all I/O happens in the caller.
//
// A candidate whose existing group is headed by something else is dropped
// rather than pooled. An existing group wins the election
// (decideGroup), so pooling that candidate would attach the seed to
// whatever that group is about — a service in another namespace, say — on the
// strength of one co-located kill. Links persist after the kill switch is
// flipped, so the node axis must never bridge two groups.
//
// A node-level member leads: every pod member, and the seed when it is a pod, is
// counted as depending on it. A pod OOM-killed by a full node is a symptom of
// that node. A seed that is the node is scored the same way, under seedKey, so a
// MemoryPressure arriving after its kills have grouped can take the lead
// (decideGroup).
func poolNodeMembers(seedKey string, seedIsNode bool, cands []nodeCandidate) ([]groupCandidate, map[string]int, bool) {
	members := make([]groupCandidate, 0, len(cands))
	subjects := map[string]bool{}
	nodeIDs := []string{}
	pods := 0
	if !seedIsNode {
		pods++
	}
	capped := false
	for _, c := range cands {
		if c.subjectKey == seedKey || !c.leaderAllowed {
			continue
		}
		if !subjects[c.subjectKey] {
			if len(subjects) >= incidentGroupSubjectCap {
				capped = true
				continue
			}
			subjects[c.subjectKey] = true
		}
		members = append(members, c.candidate)
		if c.isNode {
			nodeIDs = append(nodeIDs, c.candidate.ID)
		} else {
			pods++
		}
	}
	dependedOnBy := make(map[string]int, len(nodeIDs)+1)
	for _, id := range nodeIDs {
		dependedOnBy[id] = pods
	}
	if seedIsNode && pods > 0 {
		dependedOnBy[seedKey] = pods
	}
	return members, dependedOnBy, capped
}

// nodePool is what the node axis contributes to the seed's election.
type nodePool struct {
	node         string
	members      []groupCandidate
	memberStarts map[string]time.Time
	dependedOnBy map[string]int
}

// collectNodeMembers gathers the memory failures on the seed's node that fired in
// the attach window — on either side of the seed's start, like every other
// relation — for the caller to merge with the other related alerts before one
// head is chosen. Returns an empty pool, without querying, when the seed is not
// itself a memory failure on a node.
//
// alertRows gives every stored row of an alert by its key (alertKey), so that a
// candidate's existing group is found whichever of its rows carries the link.
func collectNodeMembers(
	ctx context.Context,
	db sqlx.ExtContext,
	event *models.Event,
	seed groupCandidate,
	seedKey string,
	start time.Time,
	alertRows map[string][]string,
) (nodePool, error) {
	var empty nodePool
	if !nodeGroupingEnabled() {
		return empty, nil
	}
	seedNode, seedIsNode, ok := nodeMemoryFailure(nodeFailure{
		AggregationKey: deref(event.AggregationKey),
		SubjectType:    deref(event.SubjectType),
		SubjectName:    deref(event.SubjectName),
		SubjectNode:    deref(event.SubjectNode),
		Evidences:      event.Evidences,
	})
	if !ok {
		return empty, nil
	}

	// Same shape as the other window queries: alerts that FIRED in the window, one
	// row per alert carrying its latest firing. Narrowed to the two memory-failure
	// keys on this node, so it returns a handful of rows; the evidence the verdict
	// is read from comes with them.
	type candidateRow struct {
		ID               string       `db:"id"`
		SubjectType      *string      `db:"subject_type"`
		SubjectName      *string      `db:"subject_name"`
		SubjectNamespace *string      `db:"subject_namespace"`
		SubjectOwner     *string      `db:"subject_owner"`
		SubjectNode      *string      `db:"subject_node"`
		AggregationKey   *string      `db:"aggregation_key"`
		Fingerprint      *string      `db:"fingerprint"`
		Evidences        *models.Json `db:"evidences"`
		StartsAt         time.Time    `db:"starts_at"`
		LastSeen         time.Time    `db:"last_seen"`
	}
	var rows []candidateRow
	err := sqlx.SelectContext(ctx, db, &rows, `
		SELECT DISTINCT ON (e.fingerprint)
		       e.id, e.subject_type, e.subject_name, e.subject_namespace, e.subject_owner,
		       e.subject_node, e.aggregation_key, e.fingerprint, e.evidences, e.starts_at,
		       max(e.starts_at) OVER (PARTITION BY e.fingerprint) AS last_seen
		FROM events e
		WHERE e.tenant = $1
		  AND e.cloud_account_id = $2
		  AND e.starts_at >= $3 AND e.starts_at <= $4
		  AND e.id != $5
		  AND e.fingerprint IS DISTINCT FROM $6
		  AND e.aggregation_key = ANY($7)
		  AND (lower(btrim(e.subject_node)) = $8
		       OR (lower(btrim(e.subject_type)) = 'node' AND lower(btrim(e.subject_name)) = $8))
		ORDER BY e.fingerprint, e.starts_at DESC
		LIMIT `+fmt.Sprint(incidentCandidateLimit),
		*event.Tenant, *event.CloudAccountId,
		start.Add(-IncidentAttachWindow), start.Add(IncidentAttachWindow), seed.ID, event.Fingerprint,
		pq.Array(NodeMemoryAggregationKeys()), seedNode,
	)
	if err != nil {
		return empty, fmt.Errorf("failed to load node candidates: %w", err)
	}

	cands := make([]nodeCandidate, 0, len(rows))
	for _, r := range rows {
		node, isNode, ok := nodeMemoryFailure(nodeFailure{
			AggregationKey: deref(r.AggregationKey),
			SubjectType:    deref(r.SubjectType),
			SubjectName:    deref(r.SubjectName),
			SubjectNode:    deref(r.SubjectNode),
			Evidences:      r.Evidences,
		})
		if !ok || node != seedNode {
			continue
		}
		cands = append(cands, nodeCandidate{
			candidate: groupCandidate{
				ID:             r.ID,
				AggregationKey: deref(r.AggregationKey),
				Fingerprint:    deref(r.Fingerprint),
				StartsAt:       r.StartsAt,
				LastSeen:       r.LastSeen,
			},
			subjectKey: SubjectKey(AlertIdentity{
				ID:               r.ID,
				SubjectType:      deref(r.SubjectType),
				SubjectName:      deref(r.SubjectName),
				SubjectNamespace: deref(r.SubjectNamespace),
				SubjectOwner:     deref(r.SubjectOwner),
			}),
			isNode:        isNode,
			leaderAllowed: true,
		})
	}
	if len(cands) == 0 {
		return empty, nil
	}

	// Which of them already belong to a group, and what heads it. A group headed
	// by a memory failure on this same node is this incident; anything else is a
	// different one, and poolNodeMembers drops the candidate.
	ids := make([]string, 0, len(cands))
	candidateOf := make(map[string]int, len(cands))
	for i, c := range cands {
		rows := append([]string{c.candidate.ID}, alertRows[alertKey(c.candidate.Fingerprint, c.candidate.ID)]...)
		for _, row := range rows {
			if _, seen := candidateOf[row]; !seen {
				candidateOf[row] = i
				ids = append(ids, row)
			}
		}
	}
	type leaderRow struct {
		MemberID       string  `db:"member_id"`
		AggregationKey *string `db:"aggregation_key"`
		SubjectType    *string `db:"subject_type"`
		SubjectName    *string `db:"subject_name"`
		SubjectNode    *string `db:"subject_node"`
	}
	var leaders []leaderRow
	err = sqlx.SelectContext(ctx, db, &leaders, `
		SELECT c.event_id AS member_id, l.aggregation_key, l.subject_type, l.subject_name, l.subject_node
		FROM event_correlations c
		JOIN events l ON l.id = c.related_event_id
		WHERE c.correlation_type = $1
		  AND c.cloud_account_id = $2
		  AND c.event_id = ANY($3)`,
		SameIncidentCorrelationType, *event.CloudAccountId, pq.Array(ids),
	)
	if err != nil {
		return empty, fmt.Errorf("failed to load node candidates' group leaders: %w", err)
	}
	for _, l := range leaders {
		if !leaderOnSameNode(deref(l.AggregationKey), deref(l.SubjectType), deref(l.SubjectName), deref(l.SubjectNode), seedNode) {
			cands[candidateOf[l.MemberID]].leaderAllowed = false
		}
	}

	members, dependedOnBy, capped := poolNodeMembers(seedKey, seedIsNode, cands)
	if capped {
		slog.InfoContext(ctx, "Node incident group hit the subject cap; not adding more",
			"event_id", event.Id, "cap", incidentGroupSubjectCap, "node", seedNode)
	}
	memberStarts := make(map[string]time.Time, len(members))
	for _, m := range members {
		memberStarts[m.ID] = m.StartsAt
	}
	return nodePool{node: seedNode, members: members, memberStarts: memberStarts, dependedOnBy: dependedOnBy}, nil
}

// leaderOnSameNode reports whether an existing group's leader is an OOM kill or a
// node_pressure on the seed's node, by key and node only. Its own verdict is not
// re-read: that group may have formed on the leader's own subject — a pod's
// earlier kill heading its later ones — and a same-node kill is a headline the
// node incident can share.
func leaderOnSameNode(aggregationKey, subjectType, subjectName, subjectNode, node string) bool {
	if aggregationKey != oomKillAggregationKey && aggregationKey != nodePressureAggregationKey {
		return false
	}
	leaderNode := strings.TrimSpace(subjectNode)
	if leaderNode == "" && strings.EqualFold(strings.TrimSpace(subjectType), "node") {
		leaderNode = strings.TrimSpace(subjectName)
	}
	return strings.EqualFold(leaderNode, node)
}
