package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"nudgebee/services/internal/database/models"
	"nudgebee/services/internal/testenv"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noisyCard is the noisy-neighbours evidence card as the enricher stores it.
func noisyCard(cause string) map[string]any {
	return map[string]any{
		"type":            "json",
		"additional_info": map[string]any{"action_name": "noisy_neighbours_enricher", "title": "Noisy Neighbours"},
		"data":            fmt.Sprintf(`{"name":"noisy_neighbours","data":{"node_name":"n","oom_cause":%q}}`, cause),
	}
}

// nodeSnapshotCard is the raw node object the agent attaches to node_pressure,
// trimmed to the conditions the parser reads.
func nodeSnapshotCard(memory, disk string) map[string]any {
	return map[string]any{
		"type": "json",
		"data": fmt.Sprintf(`{"operation":"update","kind":"Node","obj":{"status":{"conditions":[`+
			`{"type":"SysctlChanged","status":"True"},`+
			`{"type":"MemoryPressure","reason":"KubeletHasInsufficientMemory","status":%q},`+
			`{"type":"DiskPressure","status":%q},{"type":"PIDPressure","status":"False"},`+
			`{"type":"Ready","status":"True"}]}}}`, memory, disk),
	}
}

func evidenceOf(cards ...map[string]any) *models.Json {
	arr := make([]any, len(cards))
	for i, c := range cards {
		arr[i] = c
	}
	j := models.NewJsonArray(arr)
	return &j
}

func TestNodeMemoryFailure(t *testing.T) {
	cases := []struct {
		name       string
		in         nodeFailure
		wantNode   string
		wantIsNode bool
		wantOK     bool
	}{
		{
			name:     "OOM the card blames on the node",
			in:       nodeFailure{AggregationKey: oomKillAggregationKey, SubjectType: "pod", SubjectNode: "Node-1", Evidences: evidenceOf(noisyCard("node_memory"))},
			wantNode: "node-1", wantOK: true,
		},
		{
			name: "OOM at its own limit",
			in:   nodeFailure{AggregationKey: oomKillAggregationKey, SubjectNode: "node-1", Evidences: evidenceOf(noisyCard("own_limit"))},
		},
		{
			name: "OOM the card could not attribute",
			in:   nodeFailure{AggregationKey: oomKillAggregationKey, SubjectNode: "node-1", Evidences: evidenceOf(noisyCard("undetermined"))},
		},
		{
			// Stored before the enricher recorded a verdict.
			name: "OOM with no verdict on its card",
			in:   nodeFailure{AggregationKey: oomKillAggregationKey, SubjectNode: "node-1", Evidences: evidenceOf(noisyCard(""))},
		},
		{
			name: "OOM with no node",
			in:   nodeFailure{AggregationKey: oomKillAggregationKey, Evidences: evidenceOf(noisyCard("node_memory"))},
		},
		{
			// The agent names the node as the subject and leaves subject_node empty.
			name:     "node_pressure on memory",
			in:       nodeFailure{AggregationKey: nodePressureAggregationKey, SubjectType: "node", SubjectName: "node-1", Evidences: evidenceOf(nodeSnapshotCard("True", "False"))},
			wantNode: "node-1", wantIsNode: true, wantOK: true,
		},
		{
			name:     "node_pressure with subject_node filled",
			in:       nodeFailure{AggregationKey: nodePressureAggregationKey, SubjectType: "node", SubjectName: "other", SubjectNode: "node-1", Evidences: evidenceOf(nodeSnapshotCard("True", "False"))},
			wantNode: "node-1", wantIsNode: true, wantOK: true,
		},
		{
			// Shares the fingerprint with memory pressure, but kills nothing for memory.
			name: "node_pressure on disk only",
			in:   nodeFailure{AggregationKey: nodePressureAggregationKey, SubjectType: "node", SubjectName: "node-1", Evidences: evidenceOf(nodeSnapshotCard("False", "True"))},
		},
		{
			// The node is only where the pod landed.
			name: "ImagePullBackOff on the node",
			in:   nodeFailure{AggregationKey: "image_pull_backoff_reporter", SubjectNode: "node-1", Evidences: evidenceOf(noisyCard("node_memory"))},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, isNode, ok := nodeMemoryFailure(tc.in)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantNode, node)
			assert.Equal(t, tc.wantIsNode, isNode)
		})
	}
}

// The read side asks the same question through the exported wrapper.
func TestNodeMemoryNode(t *testing.T) {
	assert.Equal(t, "node-1", NodeMemoryNode(oomKillAggregationKey, "pod", "p", "Node-1", evidenceOf(noisyCard("node_memory"))))
	assert.Equal(t, "", NodeMemoryNode(oomKillAggregationKey, "pod", "p", "node-1", evidenceOf(noisyCard("own_limit"))))
	assert.Equal(t, "node-1", NodeMemoryNode(nodePressureAggregationKey, "node", "node-1", "", evidenceOf(nodeSnapshotCard("True", "False"))))
	assert.Equal(t, "", NodeMemoryNode("KubePodCrashLooping", "pod", "p", "node-1", nil))
	assert.ElementsMatch(t, []string{oomKillAggregationKey, nodePressureAggregationKey}, NodeMemoryAggregationKeys())
}

func TestOOMCauseFromEvidence(t *testing.T) {
	other := map[string]any{"type": "json", "additional_info": map[string]any{"action_name": "pod_metric_enricher"},
		"data": `{"name":"pod_metric","data":{"oom_cause":"node_memory"}}`}

	assert.Equal(t, "", oomCauseFromEvidence(nil))
	assert.Equal(t, "", oomCauseFromEvidence(evidenceOf(other)), "only the noisy-neighbours card speaks for the kill")
	assert.Equal(t, "node_memory", oomCauseFromEvidence(evidenceOf(other, noisyCard("node_memory"))))
	assert.Equal(t, "own_limit", oomCauseFromEvidence(evidenceOf(noisyCard("node_memory"), noisyCard("own_limit"))),
		"a refreshed investigation appends a newer card, and the newer one wins")
}

func TestMemoryPressureFromEvidence(t *testing.T) {
	assert.True(t, memoryPressureFromEvidence(evidenceOf(nodeSnapshotCard("True", "False"))))
	assert.True(t, memoryPressureFromEvidence(evidenceOf(nodeSnapshotCard("True", "True"))))
	assert.False(t, memoryPressureFromEvidence(evidenceOf(nodeSnapshotCard("False", "True"))))
	assert.False(t, memoryPressureFromEvidence(nil))

	// A pod snapshot mentioning the word is not a node condition.
	pod := map[string]any{"type": "json", "data": `{"kind":"Pod","obj":{"status":{"conditions":[{"type":"MemoryPressure","status":"True"}]}}}`}
	assert.False(t, memoryPressureFromEvidence(evidenceOf(pod)))
}

func TestPoolNodeMembers(t *testing.T) {
	now := time.Date(2026, 9, 29, 18, 1, 21, 0, time.UTC)
	cand := func(id, subject string, isNode, allowed bool) nodeCandidate {
		return nodeCandidate{candidate: gc(id, oomKillAggregationKey, now.Add(-time.Minute)), subjectKey: subject, isNode: isNode, leaderAllowed: allowed}
	}

	t.Run("pools other subjects on the node, skips the seed's own", func(t *testing.T) {
		members, dependedOnBy, capped := poolNodeMembers("agent|node-agent", false, []nodeCandidate{
			cand("metrics-db", "monitoring|metrics-db", false, true),
			cand("own", "agent|node-agent", false, true),
		})
		require.Len(t, members, 1)
		assert.Equal(t, "metrics-db", members[0].ID)
		assert.Empty(t, dependedOnBy)
		assert.False(t, capped)
	})

	t.Run("a member whose group is about something else is dropped", func(t *testing.T) {
		members, _, _ := poolNodeMembers("a|seed", false, []nodeCandidate{
			cand("elsewhere", "b|x", false, false),
			cand("here", "c|y", false, true),
		})
		require.Len(t, members, 1)
		assert.Equal(t, "here", members[0].ID)
	})

	t.Run("the node leads the pods it killed", func(t *testing.T) {
		_, dependedOnBy, _ := poolNodeMembers("a|seed", false, []nodeCandidate{
			cand("oom-1", "b|x", false, true),
			cand("pressure", "|node-1", true, true),
			cand("oom-2", "c|y", false, true),
		})
		assert.Equal(t, map[string]int{"pressure": 3}, dependedOnBy, "two pod members plus the pod seed")
	})

	t.Run("subject cap", func(t *testing.T) {
		cands := make([]nodeCandidate, 0, incidentGroupSubjectCap+5)
		for i := 0; i < incidentGroupSubjectCap+5; i++ {
			cands = append(cands, cand(fmt.Sprintf("oom-%d", i), fmt.Sprintf("ns|w%d", i), false, true))
		}
		members, _, capped := poolNodeMembers("a|seed", false, cands)
		assert.Len(t, members, incidentGroupSubjectCap)
		assert.True(t, capped)
	})
}

func TestLeaderOnSameNode(t *testing.T) {
	assert.True(t, leaderOnSameNode(oomKillAggregationKey, "pod", "metrics-db", "Node-1", "node-1"))
	assert.True(t, leaderOnSameNode(nodePressureAggregationKey, "node", "node-1", "", "node-1"))
	assert.False(t, leaderOnSameNode(oomKillAggregationKey, "pod", "metrics-db", "node-2", "node-1"), "another node")
	assert.False(t, leaderOnSameNode("KubePodCrashLooping", "pod", "checkout", "node-1", "node-1"), "not a memory failure")
}

// With the switch off, or for anything that is not a memory failure on a node,
// the node axis must not touch the database: a nil db proves no query ran.
func TestCollectNodeMembers_NoQueryUnlessEligible(t *testing.T) {
	now := time.Now()
	oom := &models.Event{
		Id: "e1", Tenant: strPtr("t"), CloudAccountId: strPtr("a"), StartsAt: &now,
		AggregationKey: strPtr(oomKillAggregationKey), SubjectType: strPtr("pod"),
		SubjectName: strPtr("p"), SubjectNode: strPtr("node-1"), Evidences: evidenceOf(noisyCard("node_memory")),
	}

	t.Setenv(nodeGroupingEnvFlag, "false")
	pool, err := collectNodeMembers(context.Background(), nil, oom, gc("e1", oomKillAggregationKey, now), "ns|p", now, nil)
	require.NoError(t, err)
	assert.Empty(t, pool.members)

	t.Setenv(nodeGroupingEnvFlag, "")
	ownLimit := *oom
	ownLimit.Evidences = evidenceOf(noisyCard("own_limit"))
	pool, err = collectNodeMembers(context.Background(), nil, &ownLimit, gc("e1", oomKillAggregationKey, now), "ns|p", now, nil)
	require.NoError(t, err)
	assert.Empty(t, pool.members)
}

func TestNodeGroupingEnabled(t *testing.T) {
	t.Setenv(nodeGroupingEnvFlag, "")
	assert.True(t, nodeGroupingEnabled(), "on by default")
	t.Setenv(nodeGroupingEnvFlag, "FALSE")
	assert.False(t, nodeGroupingEnabled())
	t.Setenv(nodeGroupingEnvFlag, "0")
	assert.False(t, nodeGroupingEnabled())
}

// The reported case end to end against a real database: two pods in different
// namespaces killed seconds apart by a full node become one incident; a pod on
// that node at its own limit, an ImagePullBackOff there, and a kill on another
// node stay out; the node's own MemoryPressure joins.
func TestAttachNodeMemoryIncident_E2E(t *testing.T) {
	if os.Getenv("TEST_LIVE_CORRELATION") != "1" {
		t.Skip("set TEST_LIVE_CORRELATION=1 to run (requires APP_DATABASE_URL + TEST_ACCOUNT_ID)")
	}
	env := testenv.RequireEnv(t, "TEST_ACCOUNT_ID")
	account := env["TEST_ACCOUNT_ID"]
	dbURL := os.Getenv("APP_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set APP_DATABASE_URL to run")
	}
	dbConn, err := sqlx.Connect("postgres", dbURL)
	require.NoError(t, err)
	defer func() { _ = dbConn.Close() }()
	ctx := context.Background()

	var tenant string
	require.NoError(t, dbConn.GetContext(ctx, &tenant,
		`SELECT tenant::text FROM cloud_accounts WHERE id = $1`, account))

	tx, err := dbConn.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	const node = "node-e2e-memory-1"
	anchor := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)

	mk := func(ns, owner, subjectType, subjectNode, aggKey string, startsAt time.Time, cards ...map[string]any) *models.Event {
		id := uuid.NewString()
		name := owner + "-abc12"
		if subjectType == "node" {
			name = owner
		}
		ev := evidenceOf(cards...)
		raw, err := json.Marshal(ev)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO events (id, tenant, cloud_account_id, aggregation_key, subject_type,
				subject_namespace, subject_name, subject_owner, subject_node, fingerprint, finding_id,
				finding_type, priority, cluster, starts_at, created_at, source, title, evidences)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'issue', 'HIGH', 'e2e-cluster', $12, $12,
				'kubernetes_api_server', $4, $13::jsonb)`,
			id, tenant, account, aggKey, subjectType, ns, name, owner, subjectNode, "fp-"+id, "fid-"+id, startsAt, string(raw))
		require.NoError(t, err)
		return &models.Event{
			Id: id, Tenant: &tenant, CloudAccountId: &account, Source: strPtr("kubernetes_api_server"),
			AggregationKey: strPtr(aggKey), SubjectType: strPtr(subjectType), SubjectNamespace: strPtr(ns),
			SubjectName: strPtr(name), SubjectOwner: strPtr(owner), SubjectNode: strPtr(subjectNode),
			Fingerprint: strPtr("fp-" + id), StartsAt: &startsAt, Evidences: ev,
		}
	}
	attach := func(e *models.Event) bool {
		child, err := attachSameSubjectIncident(ctx, tx, e)
		require.NoError(t, err)
		return child
	}
	linkOf := func(eventID string) (leader, reason string, ok bool) {
		var row struct {
			Leader string `db:"related_event_id"`
			Reason string `db:"correlation_reason"`
		}
		if err := tx.GetContext(ctx, &row, `
			SELECT related_event_id, correlation_reason FROM event_correlations
			WHERE event_id = $1 AND correlation_type = $2`, eventID, SameIncidentCorrelationType); err != nil {
			return "", "", false
		}
		return row.Leader, row.Reason, true
	}

	metricsDB := mk("e2e-monitoring", "metrics-db", "pod", node, oomKillAggregationKey, anchor, noisyCard("node_memory"))
	assert.False(t, attach(metricsDB), "first kill has nothing to join")

	agent := mk("e2e-agent", "log-agent", "pod", node, oomKillAggregationKey, anchor.Add(10*time.Second), noisyCard("node_memory"))
	require.True(t, attach(agent), "second kill on the full node joins the first")
	leader, reason, ok := linkOf(agent.Id)
	require.True(t, ok)
	assert.Equal(t, metricsDB.Id, leader)
	assert.True(t, strings.HasPrefix(reason, nodeMemoryReasonPrefix), reason)

	atLimit := mk("e2e-billing", "billing", "pod", node, oomKillAggregationKey, anchor.Add(20*time.Second), noisyCard("own_limit"))
	assert.False(t, attach(atLimit), "a pod at its own limit is its own problem")

	pull := mk("e2e-shop", "shop", "pod", node, "image_pull_backoff_reporter", anchor.Add(30*time.Second))
	assert.False(t, attach(pull), "the node is incidental to an image pull")

	elsewhere := mk("e2e-other", "other", "pod", "node-e2e-memory-2", oomKillAggregationKey, anchor.Add(40*time.Second), noisyCard("node_memory"))
	assert.False(t, attach(elsewhere), "another node's kill is another incident")

	// The node's own MemoryPressure arrives last. Both kills are symptoms of it, so
	// it takes the lead of the group they formed rather than being filed under the
	// first kill — still one incident, now headed by its cause.
	pressure := mk("", node, "node", "", nodePressureAggregationKey, anchor.Add(50*time.Second), nodeSnapshotCard("True", "False"))
	assert.False(t, attach(pressure), "the node's MemoryPressure heads its kills, so it is not a child")
	_, _, ok = linkOf(pressure.Id)
	assert.False(t, ok, "the leader carries no link")
	leader, reason, ok = linkOf(agent.Id)
	require.True(t, ok)
	assert.Equal(t, pressure.Id, leader, "the kill moved with the group")
	assert.True(t, strings.HasPrefix(reason, nodeMemoryReasonPrefix), "a moved link keeps the reason it joined on: %s", reason)
	leader, reason, ok = linkOf(metricsDB.Id)
	require.True(t, ok, "the first kill, which led, is now one of the children")
	assert.Equal(t, pressure.Id, leader, "joins the open group rather than starting another")
	assert.True(t, strings.HasPrefix(reason, leaderTakeoverReasonPrefix), reason)
}
