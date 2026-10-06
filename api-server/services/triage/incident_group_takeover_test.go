package triage

import (
	"context"
	"os"
	"testing"
	"time"

	"nudgebee/services/internal/database/models"
	"nudgebee/services/internal/testenv"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The arriving alert takes the lead when more of the group depends on it than on
// the leader it was about to be filed under. These pin every way that must NOT
// happen, because a wrong takeover rewrites stored links.

func TestLeaderTakeoverEnabled(t *testing.T) {
	t.Setenv(leaderTakeoverEnvFlag, "")
	assert.True(t, leaderTakeoverEnabled(), "on by default")
	t.Setenv(leaderTakeoverEnvFlag, "FALSE")
	assert.False(t, leaderTakeoverEnabled())
	t.Setenv(leaderTakeoverEnvFlag, "0")
	assert.False(t, leaderTakeoverEnabled())
}

func TestInGroupTx_RejectsAnUnknownHandle(t *testing.T) {
	err := inGroupTx(context.Background(), nil, func(*sqlx.Tx) error { return nil })
	require.Error(t, err, "a handle that is neither the pool nor a transaction cannot hold the lock")
}

// TestLeaderTakeover_E2E replays the reported case through the real SQL inside an
// always-rolled-back transaction: two callers alert and group, then the database
// they both depend on alerts four minutes later.
func TestLeaderTakeover_E2E(t *testing.T) {
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

	anchor := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)

	// payment calls order, and both call the database.
	serviceMap := func(ns string, edges string) string {
		return `[{"type": "knowledge_graph", "nodes": [
		  {"id": "order", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "order", "namespace": "` + ns + `"}},
		  {"id": "payment", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "payment", "namespace": "` + ns + `"}},
		  {"id": "inventory", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "inventory", "namespace": "` + ns + `"}},
		  {"id": "database", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "database", "namespace": "` + ns + `"}}
		], "edges": [` + edges + `]}]`
	}
	const realEdges = `
	  {"relationship_type": "CALLS", "source_node_id": "payment", "dest_node_id": "order"},
	  {"relationship_type": "CALLS", "source_node_id": "payment", "dest_node_id": "database"},
	  {"relationship_type": "CALLS", "source_node_id": "order", "dest_node_id": "database"}`

	mkEvent := func(ns, owner, aggKey, fingerprint string, startsAt time.Time, evidenceJSON string) *models.Event {
		id := uuid.NewString()
		if fingerprint == "" {
			fingerprint = "fp-" + id
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (id, tenant, cloud_account_id, aggregation_key,
				subject_namespace, subject_name, subject_owner, fingerprint, finding_id,
				finding_type, priority, cluster, starts_at, created_at, source, title, evidences)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $11, 'issue', 'HIGH', 'e2e-cluster', $9, $9, 'kubernetes_api_server', $4, coalesce(nullif($10,'')::jsonb, '[]'::jsonb))`,
			id, tenant, account, aggKey, ns, owner+"-abc12", owner, fingerprint, startsAt, evidenceJSON, "fid-"+id)
		require.NoError(t, err)
		ev := &models.Event{
			Id:               id,
			Tenant:           &tenant,
			CloudAccountId:   &account,
			AggregationKey:   strPtr(aggKey),
			SubjectNamespace: strPtr(ns),
			SubjectName:      strPtr(owner + "-abc12"),
			SubjectOwner:     strPtr(owner),
			Fingerprint:      strPtr(fingerprint),
			StartsAt:         &startsAt,
		}
		if evidenceJSON != "" {
			var j models.Json
			require.NoError(t, j.Scan([]uint8(evidenceJSON)))
			ev.Evidences = &j
		}
		return ev
	}
	linksOf := func(eventID string) []takeoverLink {
		var ls []takeoverLink
		require.NoError(t, tx.SelectContext(ctx, &ls, `
			SELECT related_event_id, correlation_reason FROM event_correlations
			WHERE event_id = $1 AND correlation_type = $2`, eventID, SameIncidentCorrelationType))
		return ls
	}
	childrenOf := func(eventID string) int {
		var n int
		require.NoError(t, tx.GetContext(ctx, &n, `
			SELECT count(*) FROM event_correlations
			WHERE related_event_id = $1 AND correlation_type = $2`, eventID, SameIncidentCorrelationType))
		return n
	}
	attach := func(ev *models.Event) bool {
		child, err := attachSameSubjectIncident(ctx, tx, ev)
		require.NoError(t, err)
		return child
	}
	// The co-timing path would otherwise answer some of these on its own.
	t.Setenv(unlocatedGroupingEnvFlag, "false")

	const ns = "ns-e2e-takeover"
	kg := serviceMap(ns, realEdges)

	// The callers alert first and group under order.
	order := mkEvent(ns, "order", "ConnectionsPending", "fp-order-pending-"+ns, anchor, kg)
	require.False(t, attach(order), "first alert opens the group")
	payment := mkEvent(ns, "payment", "ApplicationSlow", "", anchor.Add(time.Minute), kg)
	require.True(t, attach(payment))
	require.Equal(t, []string{order.Id}, leadersOf(linksOf(payment.Id)), "payment calls order, so order leads the callers")

	// The database alert arrives four minutes later and takes the lead.
	database := mkEvent(ns, "database", "DatabaseDown", "fp-database-down-"+ns, anchor.Add(4*time.Minute), kg)
	assert.False(t, attach(database), "the new leader is not a child")
	assert.Empty(t, linksOf(database.Id))
	assert.Equal(t, []string{database.Id}, leadersOf(linksOf(payment.Id)), "the child moved with the star and has one parent")
	demoted := linksOf(order.Id)
	require.Len(t, demoted, 1, "the old leader is one more child")
	assert.Equal(t, database.Id, demoted[0].Leader)
	assert.Contains(t, demoted[0].Reason, leaderTakeoverReasonPrefix)
	assert.Zero(t, childrenOf(order.Id), "nothing is left under the replaced leader")
	assert.Equal(t, 2, childrenOf(database.Id))
	var paymentOffset int
	require.NoError(t, tx.GetContext(ctx, &paymentOffset, `
		SELECT time_offset_minutes FROM event_correlations
		WHERE event_id = $1 AND correlation_type = $2`, payment.Id, SameIncidentCorrelationType))
	assert.Equal(t, -3, paymentOffset, "a moved link's offset is measured from the new leader: payment started three minutes before it")

	// Another alert on the database itself ties with the head on dependents (it
	// sits on the same service), so it joins and the head stays.
	lag := mkEvent(ns, "database", "ReplicationLag", "", anchor.Add(4*time.Minute), kg)
	assert.True(t, attach(lag))
	assert.Equal(t, []string{database.Id}, leadersOf(linksOf(lag.Id)))
	assert.Empty(t, linksOf(database.Id), "a tie does not move the group")

	// The database alert re-notifies: the new row is filed under the row that
	// leads, and is not scored as a symptom of its own alert.
	databaseAgain := mkEvent(ns, "database", "DatabaseDown", "fp-database-down-"+ns, anchor.Add(6*time.Minute), kg)
	assert.False(t, attach(databaseAgain), "another row of the head's alert is not a child")
	assert.Equal(t, []string{database.Id}, leadersOf(linksOf(databaseAgain.Id)))

	// The replaced leader fires again carrying a map that says the opposite —
	// everything depends on order. On score alone it would take the lead back;
	// its alert is already filed under this leader, so it does not.
	orderLeads := serviceMap(ns, `
	  {"relationship_type": "CALLS", "source_node_id": "payment", "dest_node_id": "order"},
	  {"relationship_type": "CALLS", "source_node_id": "database", "dest_node_id": "order"}`)
	orderAgain := mkEvent(ns, "order", "ConnectionsPending", "fp-order-pending-"+ns, anchor.Add(7*time.Minute), orderLeads)
	assert.True(t, attach(orderAgain), "filed as a child, not handed the lead back")
	assert.Equal(t, []string{database.Id}, leadersOf(linksOf(orderAgain.Id)))
	assert.Empty(t, linksOf(database.Id), "the leader stays the leader")

	// An attach that raced the takeover leaves an alert filed under the replaced
	// leader. The next alert that reaches the group through it lands on the
	// current leader, not on the replaced one.
	straggler := mkEvent(ns, "inventory", "ApplicationSlow", "", anchor.Add(5*time.Minute), "")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO event_correlations (event_id, related_event_id, cloud_account_id, tenant_id,
			correlation_type, correlation_score, correlation_reason, time_offset_minutes, dependency_distance)
		VALUES ($1, $2, $3, $4, $5, 1.0, 'raced the takeover', 5, 1)`,
		straggler.Id, order.Id, account, tenant, SameIncidentCorrelationType)
	require.NoError(t, err)
	inventoryErrors := mkEvent(ns, "inventory", "HTTPErrorRate", "", anchor.Add(8*time.Minute), "")
	assert.True(t, attach(inventoryErrors))
	assert.Equal(t, []string{database.Id}, leadersOf(linksOf(inventoryErrors.Id)), "followed the replaced leader to its replacement")

	// The replaced leader still has that one alert filed under it. It must not
	// count as a second open incident, or an alert the graph cannot place would
	// find two incidents in its namespace and join neither.
	t.Setenv(unlocatedGroupingEnvFlag, "true")
	unplaced := mkEvent(ns, "batch-job", "JobFailed", "", anchor.Add(9*time.Minute), "")
	assert.True(t, attach(unplaced), "one incident is open in the namespace, so co-timing may attach")
	assert.Equal(t, []string{database.Id}, leadersOf(linksOf(unplaced.Id)))
	t.Setenv(unlocatedGroupingEnvFlag, "false")

	// A flapper does not keep its own incident open. Its repeats join while
	// another alert in the group has fired inside the window; once only its own
	// firings are left there, the next one stands alone.
	const nsFlap = "ns-e2e-flapper"
	pull := mkEvent(nsFlap, "catalog", "ImagePullBackOff", "fp-pull-"+nsFlap, anchor, "")
	require.False(t, attach(pull))
	mismatch := mkEvent(nsFlap, "catalog", "ReplicasMismatch", "", anchor.Add(2*time.Minute), "")
	require.True(t, attach(mismatch))
	require.Equal(t, []string{pull.Id}, leadersOf(linksOf(mismatch.Id)))
	pullAgain := mkEvent(nsFlap, "catalog", "ImagePullBackOff", "fp-pull-"+nsFlap, anchor.Add(10*time.Minute), "")
	assert.False(t, attach(pullAgain), "one more row of the head, not its symptom")
	assert.Equal(t, []string{pull.Id}, leadersOf(linksOf(pullAgain.Id)), "the other alert fired eight minutes ago: the incident is still open")
	pullLater := mkEvent(nsFlap, "catalog", "ImagePullBackOff", "fp-pull-"+nsFlap, anchor.Add(20*time.Minute), "")
	assert.False(t, attach(pullLater))
	assert.Empty(t, linksOf(pullLater.Id), "only its own firings are left in the window: this one stands alone")

	// Kill switch: the same story with the takeover off files the database
	// alert under the caller, as it always was.
	t.Setenv(leaderTakeoverEnvFlag, "false")
	const nsOff = "ns-e2e-takeover-off"
	kgOff := serviceMap(nsOff, realEdges)
	orderOff := mkEvent(nsOff, "order", "ConnectionsPending", "", anchor, kgOff)
	require.False(t, attach(orderOff))
	paymentOff := mkEvent(nsOff, "payment", "ApplicationSlow", "", anchor.Add(time.Minute), kgOff)
	require.True(t, attach(paymentOff))
	databaseOff := mkEvent(nsOff, "database", "DatabaseDown", "", anchor.Add(4*time.Minute), kgOff)
	assert.True(t, attach(databaseOff), "with the switch off the arriving alert is a child")
	assert.Equal(t, []string{orderOff.Id}, leadersOf(linksOf(databaseOff.Id)))
	assert.Equal(t, []string{orderOff.Id}, leadersOf(linksOf(paymentOff.Id)))
}

// takeoverLink is one same_incident link as the E2E reads it back.
type takeoverLink struct {
	Leader string `db:"related_event_id"`
	Reason string `db:"correlation_reason"`
}

func leadersOf(links []takeoverLink) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.Leader)
	}
	return out
}

// The count the election ranks on has to be about direction. A caller depends on
// its database, never the other way round, and a distance that searches both ways
// cannot tell them apart.
func TestDependsOnDistance_FollowsDependenciesOnly(t *testing.T) {
	const (
		portal   = "shop/portal"
		order    = "shop/order"
		database = "shop/database"
		cache    = "shop/cache"
	)
	graph := &DependencyGraph{
		Nodes: map[string]*ServiceNode{portal: {}, order: {}, database: {}, cache: {}},
		Edges: map[string][]string{
			portal: {order},
			order:  {database},
		},
		ReverseEdges: map[string][]string{
			order:    {portal},
			database: {order},
		},
	}

	assert.Equal(t, 1, graph.dependsOnDistance(order, database, maxIncidentHops))
	assert.Equal(t, 2, graph.dependsOnDistance(portal, database, maxIncidentHops), "through order")
	assert.Equal(t, -1, graph.dependsOnDistance(database, order, maxIncidentHops), "the database does not depend on its caller")
	assert.Equal(t, 1, graph.getDependencyDistance(database, order), "while the membership distance sees them as connected")
	assert.Equal(t, -1, graph.dependsOnDistance(portal, database, 1), "past the hop limit")
	assert.Equal(t, -1, graph.dependsOnDistance(order, cache, maxIncidentHops), "no path")
	assert.Equal(t, 0, graph.dependsOnDistance(order, order, maxIncidentHops))
}
