package observability

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"nudgebee/services/eventrule/playbooks"
	"nudgebee/services/internal/database"
)

// When one machine runs out of memory it kills more than one tenant, seconds to
// minutes apart, and Nudgebee reported each kill as a separate unrelated problem
// (#39083). The node name has been on this card all along; nothing used it to
// connect one event to another.
//
// This is deliberately NOT a graph edge. A "same node" edge over-groups
// catastrophically — node degree runs to 1,047 in the incident graph — so the
// link here is narrow: same node, both OOM kills, inside one short window. It
// annotates the card rather than merging incidents, which is reversible and
// answers the actual complaint ("I could not tell"), not a stronger one.
const (
	// Measured over 14 days of dev OOM events: 77 of 730 had a different pod
	// killed on the same node inside 5 minutes, and widening to 15 minutes added
	// only 7 more. Kills minutes apart on one node are one story; kills hours
	// apart are a chronically undersized node, which is a different story and not
	// this card's to tell.
	coLocatedWindowMinutes = 5

	// A full node can kill a lot of pods at once. The list is for recognition,
	// not inventory — past a handful the reader already has the point, and the
	// count in the insight still reports the true total.
	coLocatedMaxListed = 10
)

// coLocatedOOMAggregationKeys is the set of events that count as "this node
// killed a pod". Kept to the kernel OOM killer on purpose: evictions and
// TerminationByKubelet are the other symptoms of a full node and plausibly
// belong here, but they have not been measured the way this one has, and a
// wrong link is worse than a missing one.
var coLocatedOOMAggregationKeys = []string{"pod_oom_killer_enricher"}

// Indirected so a test can drive the enricher without a metastore behind it.
var lookupCoLocatedOOMKills = queryCoLocatedOOMKills

func isCoLocatedOOMKey(aggregationKey string) bool {
	for _, key := range coLocatedOOMAggregationKeys {
		if key == aggregationKey {
			return true
		}
	}
	return false
}

// coKilledPod is one other pod this node killed around the same time.
type coKilledPod struct {
	// EventID is that kill's own event, so the card can link straight to it.
	// Seeing the other pod named is the fix; being able to open it is what turns
	// "one machine" from a claim into something the reader can check.
	EventID   string
	PodName   string
	Namespace string
	KilledAt  time.Time
	// OffsetSeconds is signed and relative to the subject's kill: negative means
	// it died first. "2s before" is what makes the shared cause obvious.
	OffsetSeconds int
}

// coLocatedOOMKills returns the other pods OOM-killed on this node inside the
// window. A failure here costs the annotation, never the card, so every error
// path logs and returns nothing.
func coLocatedOOMKills(ctx playbooks.PlaybookActionContext, nodeName, subjectPod string, at time.Time) []coKilledPod {
	if nodeName == "" || at.IsZero() {
		return nil
	}
	// Only for an event that is itself an OOM kill. The enricher also runs on
	// throttling and never-ready alerts, and for those the finding would be a
	// different claim ("this node is killing pods") that has not been measured
	// the way the co-located OOM case has — and that the wording below, which
	// counts this event as one of the kills, would state wrongly.
	if !isCoLocatedOOMKey(ctx.GetEvent().AggregationKey) {
		return nil
	}
	tenantID, accountID := ctx.GetTenantId(), ctx.GetAccountId()
	if tenantID == "" || accountID == "" {
		return nil
	}
	kills, err := lookupCoLocatedOOMKills(tenantID, accountID, nodeName, subjectPod, at)
	if err != nil {
		ctx.GetLogger().Warn("noisy_neighbours_enricher: co-located OOM lookup failed; card renders without the annotation",
			"error", err, "node", nodeName, "pod", subjectPod)
		return nil
	}
	return kills
}

// queryCoLocatedOOMKills reads the events table.
//
// The window predicate leads so the plan uses (tenant, created_at); the node and
// aggregation key are heap filters over the handful of rows a 10-minute slice
// holds. Measured on dev: 0.28 ms, 81 shared buffers. subject_node is populated
// on every OOM event (804/804 over 14 days) and agrees with the node this card
// resolved on 733 of 735, so it is the right column to match on.
func queryCoLocatedOOMKills(tenantID, accountID, nodeName, subjectPod string, at time.Time) ([]coKilledPod, error) {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, fmt.Errorf("metastore unavailable: %w", err)
	}

	const query = `
		SELECT id, subject_name, subject_namespace, created_at
		FROM events
		WHERE tenant = $1
		  AND cloud_account_id = $2
		  AND created_at BETWEEN $3 AND $4
		  AND aggregation_key = ANY($5)
		  AND subject_node = $6
		  AND subject_name <> $7
		  AND subject_name <> ''
		ORDER BY created_at`

	window := time.Duration(coLocatedWindowMinutes) * time.Minute
	rows, err := dbms.Db.Queryx(query, tenantID, accountID,
		at.Add(-window), at.Add(window),
		pq.StringArray(coLocatedOOMAggregationKeys), nodeName, subjectPod)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// One pod can fire more than once inside the window (a CrashLoop being
	// OOM-killed repeatedly). Keep the firing nearest the subject's kill: the
	// reader is being shown co-incidence, and the nearest one is the strongest
	// evidence of it. Counting the repeats instead would inflate "3 other pods"
	// into a number of pods that do not exist.
	nearest := map[string]coKilledPod{}
	for rows.Next() {
		var eventID, pod, namespace string
		var killedAt time.Time
		if err := rows.Scan(&eventID, &pod, &namespace, &killedAt); err != nil {
			return nil, err
		}
		offset := int(killedAt.Sub(at).Round(time.Second).Seconds())
		key := namespace + "/" + pod
		if existing, seen := nearest[key]; seen && abs(existing.OffsetSeconds) <= abs(offset) {
			continue
		}
		nearest[key] = coKilledPod{
			EventID:       eventID,
			PodName:       pod,
			Namespace:     namespace,
			KilledAt:      killedAt,
			OffsetSeconds: offset,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	kills := make([]coKilledPod, 0, len(nearest))
	for _, k := range nearest {
		kills = append(kills, k)
	}
	// Nearest the subject's own kill first — that ordering is the argument.
	sort.Slice(kills, func(i, j int) bool {
		if abs(kills[i].OffsetSeconds) != abs(kills[j].OffsetSeconds) {
			return abs(kills[i].OffsetSeconds) < abs(kills[j].OffsetSeconds)
		}
		return kills[i].Namespace+"/"+kills[i].PodName < kills[j].Namespace+"/"+kills[j].PodName
	})
	return kills, nil
}

// coKilledPayload renders the list for the card, capped for display. The cap is
// on the rendered rows only; callers count the full slice.
func coKilledPayload(kills []coKilledPod) []map[string]any {
	listed := kills
	if len(listed) > coLocatedMaxListed {
		listed = listed[:coLocatedMaxListed]
	}
	out := make([]map[string]any, 0, len(listed))
	for _, k := range listed {
		out = append(out, map[string]any{
			"event_id":       k.EventID,
			"pod_name":       k.PodName,
			"namespace":      k.Namespace,
			"killed_at":      k.KilledAt.UTC().Format(time.RFC3339),
			"offset_seconds": k.OffsetSeconds,
		})
	}
	return out
}

// describeCoKilled renders the insight's pod list, e.g.
// "victoria/vmsingle-… (2s before), demo/kafka-… (1m after)".
func describeCoKilled(kills []coKilledPod) string {
	listed := kills
	truncated := 0
	if len(listed) > coLocatedMaxListed {
		truncated = len(listed) - coLocatedMaxListed
		listed = listed[:coLocatedMaxListed]
	}
	parts := make([]string, 0, len(listed))
	for _, k := range listed {
		parts = append(parts, fmt.Sprintf("%s/%s (%s)", k.Namespace, k.PodName, relativeOffset(k.OffsetSeconds)))
	}
	joined := strings.Join(parts, ", ")
	if truncated > 0 {
		joined = fmt.Sprintf("%s and %d more", joined, truncated)
	}
	return joined
}

// relativeOffset turns a signed second count into "2s before" / "1m 5s after".
// "at the same moment" is reserved for a genuine tie, which on a node that ran
// out of memory is the common case and worth saying outright.
func relativeOffset(seconds int) string {
	if seconds == 0 {
		return "at the same moment"
	}
	direction := "after"
	if seconds < 0 {
		direction = "before"
	}
	magnitude := abs(seconds)
	if magnitude < 60 {
		return fmt.Sprintf("%ds %s", magnitude, direction)
	}
	if rem := magnitude % 60; rem != 0 {
		return fmt.Sprintf("%dm %ds %s", magnitude/60, rem, direction)
	}
	return fmt.Sprintf("%dm %s", magnitude/60, direction)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
