package triage

import (
	"context"
	"os"
	"testing"
	"time"

	"nudgebee/services/internal/database/models"
	"nudgebee/services/internal/testenv"

	"github.com/jmoiron/sqlx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gc(id, aggKey string, startsAt time.Time) groupCandidate {
	return groupCandidate{ID: id, AggregationKey: aggKey, StartsAt: startsAt}
}

func TestAttachSameSubjectIncident_DerivedSignalsNeverGroup(t *testing.T) {
	// SLO violations and anomaly detections are statistical echoes, not
	// concrete failures — they neither lead nor join groups (mirrors the
	// assembly's isDerivedSignal rule). attachSameSubjectIncident must bail
	// before touching the DB, so a nil db is the proof.
	slo := &models.Event{
		Id:               "slo-1",
		Tenant:           strPtr("t"),
		CloudAccountId:   strPtr("a"),
		AggregationKey:   strPtr("SLOViolation"),
		FindingType:      strPtr("SLO"),
		SubjectNamespace: strPtr("ns"),
		SubjectOwner:     strPtr("web"),
		StartsAt:         &time.Time{},
	}
	attached, err := attachSameSubjectIncident(context.Background(), nil, slo)
	assert.NoError(t, err)
	assert.False(t, attached)

	slo.FindingType = strPtr("Anomaly")
	attached, err = attachSameSubjectIncident(context.Background(), nil, slo)
	assert.NoError(t, err)
	assert.False(t, attached)
}

func TestIncidentGroupingEnabled(t *testing.T) {
	// On by default; only an explicit false/0 kills it.
	t.Setenv(incidentGroupingEnvFlag, "")
	assert.True(t, incidentGroupingEnabled())
	t.Setenv(incidentGroupingEnvFlag, "true")
	assert.True(t, incidentGroupingEnabled())
	t.Setenv(incidentGroupingEnvFlag, "false")
	assert.False(t, incidentGroupingEnabled())
	t.Setenv(incidentGroupingEnvFlag, "0")
	assert.False(t, incidentGroupingEnabled())
}

func TestEventAlertIdentity_NilSafe(t *testing.T) {
	id := eventAlertIdentity(&models.Event{Id: "x"})
	assert.Equal(t, "x", id.ID)
	assert.Empty(t, id.SubjectName)
	assert.Empty(t, id.SubjectOwner)
	assert.True(t, len(SubjectKey(id)) > 0 && SubjectKey(id)[len(SubjectKey(id))-1] == '|',
		"no subject identity yields a trailing-| key, which attach rejects")
}

// TestAttachSameSubjectIncident_E2E replays the OOM-pod story (OOMKilled ->
// CrashLoopBackOff -> NotReady on one workload) through the real SQL inside an
// always-rolled-back transaction (the TestLoadChronicStats_E2E pattern) and
// asserts the star shape: both later alerts link straight to the OOM leader.
func TestAttachSameSubjectIncident_E2E(t *testing.T) {
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

	const ns = "ns-e2e-incident-group"
	anchor := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)

	mkEvent := func(aggKey string, startsAt time.Time) *models.Event {
		id := uuid.NewString()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (id, tenant, cloud_account_id, aggregation_key,
				subject_namespace, subject_name, subject_owner, fingerprint, finding_id,
				finding_type, priority, cluster, starts_at, created_at, source, title, evidences)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $10, 'issue', 'HIGH', 'e2e-cluster', $9, $9, 'kubernetes_api_server', $4, '[]'::jsonb)`,
			id, tenant, account, aggKey, ns, "checkout-7d9f8b6c5d-x2vk4", "checkout", "fp-"+id, startsAt, "fid-"+id)
		require.NoError(t, err)
		return &models.Event{
			Id:               id,
			Tenant:           &tenant,
			CloudAccountId:   &account,
			AggregationKey:   strPtr(aggKey),
			SubjectNamespace: strPtr(ns),
			SubjectName:      strPtr("checkout-7d9f8b6c5d-x2vk4"),
			SubjectOwner:     strPtr("checkout"),
			Fingerprint:      strPtr("fp-" + id),
			StartsAt:         &startsAt,
		}
	}

	leaderLinkOf := func(eventID string) (string, bool) {
		var leader string
		err := tx.GetContext(ctx, &leader, `
			SELECT related_event_id FROM event_correlations
			WHERE event_id = $1 AND correlation_type = $2`, eventID, SameIncidentCorrelationType)
		if err != nil {
			return "", false
		}
		return leader, true
	}

	oom := mkEvent("KubeContainerOOMKilled", anchor)
	oomChild, err := attachSameSubjectIncident(ctx, tx, oom)
	require.NoError(t, err)
	assert.False(t, oomChild, "first alert on the subject is an implicit group of one — not a child")
	_, linked := leaderLinkOf(oom.Id)
	assert.False(t, linked, "first alert on the subject is an implicit group of one — no link row")

	crash := mkEvent("KubePodCrashLooping", anchor.Add(3*time.Minute))
	crashChild, err := attachSameSubjectIncident(ctx, tx, crash)
	require.NoError(t, err)
	assert.True(t, crashChild, "attached under the leader — scored as a child")
	leader, linked := leaderLinkOf(crash.Id)
	require.True(t, linked)
	assert.Equal(t, oom.Id, leader)

	notReady := mkEvent("KubePodNotReady", anchor.Add(6*time.Minute))
	notReadyChild, err := attachSameSubjectIncident(ctx, tx, notReady)
	require.NoError(t, err)
	assert.True(t, notReadyChild, "attached under the leader — scored as a child")
	leader, linked = leaderLinkOf(notReady.Id)
	require.True(t, linked)
	assert.Equal(t, oom.Id, leader, "third alert links to the leader, not to the second alert — star, not chain")

	// A late alert after the attach window opens a new group instead.
	late := mkEvent("KubeDeploymentReplicasMismatch", anchor.Add(6*time.Minute).Add(IncidentAttachWindow+time.Minute))
	lateChild, err := attachSameSubjectIncident(ctx, tx, late)
	require.NoError(t, err)
	assert.False(t, lateChild, "past the attach window it opens its own group — not a child")
	_, linked = leaderLinkOf(late.Id)
	assert.False(t, linked, "quiet gap past the attach window ends the group")
}

// TestAttachTopologyIncident_E2E replays the cross-service story through the
// real SQL inside an always-rolled-back transaction: checkout's alert opens a
// group; payments' alert carries a stored service map with a CALLS edge to
// checkout and must join checkout's group with a topology reason.
func TestAttachTopologyIncident_E2E(t *testing.T) {
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

	const ns = "ns-e2e-topology"
	anchor := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)

	kgEvidence := `[{"type": "knowledge_graph", "nodes": [
	  {"id": "n1", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "checkout", "namespace": "` + ns + `"}},
	  {"id": "n2", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "payments", "namespace": "` + ns + `"}}
	], "edges": [
	  {"relationship_type": "CALLS", "source_node_id": "n2", "dest_node_id": "n1"}
	]}]`

	mkEvent := func(owner, aggKey string, startsAt time.Time, evidenceJSON string) *models.Event {
		id := uuid.NewString()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (id, tenant, cloud_account_id, aggregation_key,
				subject_namespace, subject_name, subject_owner, fingerprint, finding_id,
				finding_type, priority, cluster, starts_at, created_at, source, title, evidences)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $11, 'issue', 'HIGH', 'e2e-cluster', $9, $9, 'kubernetes_api_server', $4, coalesce(nullif($10,'')::jsonb, '[]'::jsonb))`,
			id, tenant, account, aggKey, ns, owner+"-abc12", owner, "fp-"+id, startsAt, evidenceJSON, "fid-"+id)
		require.NoError(t, err)
		ev := &models.Event{
			Id:               id,
			Tenant:           &tenant,
			CloudAccountId:   &account,
			AggregationKey:   strPtr(aggKey),
			SubjectNamespace: strPtr(ns),
			SubjectName:      strPtr(owner + "-abc12"),
			SubjectOwner:     strPtr(owner),
			Fingerprint:      strPtr("fp-" + id),
			StartsAt:         &startsAt,
		}
		if evidenceJSON != "" {
			var j models.Json
			require.NoError(t, j.Scan([]uint8(evidenceJSON)))
			ev.Evidences = &j
		}
		return ev
	}

	// checkout opens its group (lone leader — no link row yet).
	checkoutErr := mkEvent("checkout", "HighErrorRate", anchor, "")
	checkoutErrChild, err := attachSameSubjectIncident(ctx, tx, checkoutErr)
	require.NoError(t, err)
	assert.False(t, checkoutErrChild, "first alert opens the group — not a child")

	// payments alerts 4 minutes later; its stored map says payments CALLS
	// checkout — it must join checkout's group via topology.
	paymentsErr := mkEvent("payments", "HighLatency", anchor.Add(4*time.Minute), kgEvidence)
	paymentsErrChild, err := attachSameSubjectIncident(ctx, tx, paymentsErr)
	require.NoError(t, err)
	assert.True(t, paymentsErrChild, "joined checkout's group by topology — scored as a child")

	var leader, reason string
	err = tx.QueryRowContext(ctx, `
		SELECT related_event_id, correlation_reason FROM event_correlations
		WHERE event_id = $1 AND correlation_type = $2`, paymentsErr.Id, SameIncidentCorrelationType).
		Scan(&leader, &reason)
	require.NoError(t, err, "payments must have topology-attached")
	assert.Equal(t, checkoutErr.Id, leader)
	// The reason has said "N hop(s) away" since the connected-set pools were
	// merged; "calls edge" was the wording of the single-hop rule that replaced.
	// The assertion kept passing review because this E2E only runs with
	// TEST_LIVE_CORRELATION=1.
	assert.Contains(t, reason, "hop(s) away")

	// A later checkout alert still resolves the same star (transitivity via
	// the edge-following leader resolution).
	checkoutCrash := mkEvent("checkout", "CrashLoopBackOff", anchor.Add(6*time.Minute), "")
	checkoutCrashChild, err := attachSameSubjectIncident(ctx, tx, checkoutCrash)
	require.NoError(t, err)
	assert.True(t, checkoutCrashChild, "same-subject attach — scored as a child")
	err = tx.QueryRowContext(ctx, `
		SELECT related_event_id FROM event_correlations
		WHERE event_id = $1 AND correlation_type = $2`, checkoutCrash.Id, SameIncidentCorrelationType).
		Scan(&leader)
	require.NoError(t, err)
	assert.Equal(t, checkoutErr.Id, leader, "same-subject attach joins the existing cross-service star")

	// No stored map on the seed and no same-subject group: no attach.
	//
	// The unlocated co-timing path is switched off for this assertion because it
	// would otherwise answer it: inventory has no topology position, checkout's
	// group is the only one open, and they share a namespace — which is exactly
	// when attachUnlocatedIncident attaches. That path has its own test; this one
	// is about topology evidence, so it must not depend on co-timing either way.
	t.Setenv(unlocatedGroupingEnvFlag, "false")
	lonely := mkEvent("inventory", "DiskPressure", anchor.Add(5*time.Minute), "")
	lonelyChild, err := attachSameSubjectIncident(ctx, tx, lonely)
	require.NoError(t, err)
	assert.False(t, lonelyChild, "no edge to the group — not a child")
	var n int
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT count(*) FROM event_correlations
		WHERE event_id = $1 AND correlation_type = $2`, lonely.Id, SameIncidentCorrelationType).Scan(&n))
	assert.Equal(t, 0, n, "no map, no same-subject members — stays lone")
}

// TestAttachUnlocatedIncident_Scoped_E2E pins the one thing that keeps the
// co-timing path from merging an account: an alert the graph cannot place joins
// an open incident only inside its own scope — its namespace for a Kubernetes
// alert, AWS as a whole for an AWS alarm.
//
// Without the namespace bound this path was the single largest producer of
// wrong links — measured on prod over 7 days, 11,633 of its 15,632 links crossed
// a namespace, and one ImagePullBackOff group had collected 1,854 members across
// 29 namespaces because a chronic leader kept it open indefinitely.
func TestAttachUnlocatedIncident_Scoped_E2E(t *testing.T) {
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

	const incidentNS = "ns-e2e-unlocated-incident"
	const otherNS = "ns-e2e-unlocated-other"
	anchor := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)

	// No evidences on any of these: every seed here reaches the unlocated path,
	// which is the path under test.
	mkSourced := func(source, ns, owner, aggKey string, startsAt time.Time) *models.Event {
		id := uuid.NewString()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (id, tenant, cloud_account_id, aggregation_key,
				subject_namespace, subject_name, subject_owner, fingerprint, finding_id,
				finding_type, priority, cluster, starts_at, created_at, source, title, evidences)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $10, 'issue', 'HIGH', 'e2e-cluster', $9, $9, $11, $4, '[]'::jsonb)`,
			id, tenant, account, aggKey, ns, owner+"-abc12", owner, "fp-"+id, startsAt, "fid-"+id, source)
		require.NoError(t, err)
		return &models.Event{
			Id:               id,
			Tenant:           &tenant,
			CloudAccountId:   &account,
			Source:           strPtr(source),
			AggregationKey:   strPtr(aggKey),
			SubjectNamespace: strPtr(ns),
			SubjectName:      strPtr(owner + "-abc12"),
			SubjectOwner:     strPtr(owner),
			Fingerprint:      strPtr("fp-" + id),
			StartsAt:         &startsAt,
		}
	}
	mkEvent := func(ns, owner, aggKey string, startsAt time.Time) *models.Event {
		return mkSourced("kubernetes_api_server", ns, owner, aggKey, startsAt)
	}
	leaderLinkOf := func(eventID string) (string, bool) {
		var leader string
		if err := tx.GetContext(ctx, &leader, `
			SELECT related_event_id FROM event_correlations
			WHERE event_id = $1 AND correlation_type = $2`, eventID, SameIncidentCorrelationType); err != nil {
			return "", false
		}
		return leader, true
	}

	// An incident is open in incidentNS: two alerts on one subject, so the group
	// exists and its leader lives in that namespace.
	oom := mkEvent(incidentNS, "checkout", "KubeContainerOOMKilled", anchor)
	_, err = attachSameSubjectIncident(ctx, tx, oom)
	require.NoError(t, err)
	crash := mkEvent(incidentNS, "checkout", "KubePodCrashLooping", anchor.Add(2*time.Minute))
	crashChild, err := attachSameSubjectIncident(ctx, tx, crash)
	require.NoError(t, err)
	require.True(t, crashChild, "precondition: an incident is open in incidentNS")

	// An unplaceable alert in ANOTHER namespace must not join it. This is the
	// regression: co-timing alone used to be enough.
	foreign := mkEvent(otherNS, "billing", "DiskPressure", anchor.Add(4*time.Minute))
	foreignChild, err := attachSameSubjectIncident(ctx, tx, foreign)
	require.NoError(t, err)
	assert.False(t, foreignChild, "another namespace's incident is not this alert's incident")
	_, linked := leaderLinkOf(foreign.Id)
	assert.False(t, linked, "no link row across namespaces on co-timing alone")

	// The same alert inside the incident's own namespace still joins — the gate
	// narrows this path, it does not disable it.
	local := mkEvent(incidentNS, "inventory", "DiskPressure", anchor.Add(5*time.Minute))
	localChild, err := attachSameSubjectIncident(ctx, tx, local)
	require.NoError(t, err)
	assert.True(t, localChild, "same namespace, sole open incident — still attaches")
	leader, linked := leaderLinkOf(local.Id)
	require.True(t, linked)
	assert.Equal(t, oom.Id, leader, "joins the open incident's leader")

	// AWS: an incident led by an EC2 alarm is open in the same window as the
	// Kubernetes one above.
	ec2CPU := mkSourced("AWS_CloudWatch_Alarm", "AmazonEC2", "i-0e2e0000000000001", "ec2-cpu-high", anchor.Add(time.Minute))
	_, err = attachSameSubjectIncident(ctx, tx, ec2CPU)
	require.NoError(t, err)
	ec2Status := mkSourced("AWS_CloudWatch_Alarm", "AmazonEC2", "i-0e2e0000000000001", "ec2-status-check", anchor.Add(3*time.Minute))
	ec2StatusChild, err := attachSameSubjectIncident(ctx, tx, ec2Status)
	require.NoError(t, err)
	require.True(t, ec2StatusChild, "precondition: an AWS incident is open")

	// An ELB alarm the graph cannot place joins it across AWS services on timing
	// — and is not confused by the Kubernetes incident open at the same time.
	elb := mkSourced("AWS_EventBridge", "AWSELB", "app/e2e-lb/1a2b3c", "alb-target-5xx", anchor.Add(6*time.Minute))
	elbChild, err := attachSameSubjectIncident(ctx, tx, elb)
	require.NoError(t, err)
	assert.True(t, elbChild, "AWS alarms group across AWS services on timing")
	leader, linked = leaderLinkOf(elb.Id)
	require.True(t, linked)
	assert.Equal(t, ec2CPU.Id, leader, "joins the AWS incident, not the Kubernetes one")

	// A Kubernetes alert never lands in the AWS incident, even in a namespace
	// that has nothing else open.
	stray := mkEvent("ns-e2e-unlocated-quiet", "cart", "DiskPressure", anchor.Add(7*time.Minute))
	strayChild, err := attachSameSubjectIncident(ctx, tx, stray)
	require.NoError(t, err)
	assert.False(t, strayChild, "the AWS incident is not a Kubernetes alert's incident")

	// Co-timing is the fallback for alerts the graph cannot place. The two seeds
	// below sit in incidentNS, where exactly one incident is open, so the old rule
	// would have attached both.
	mkWith := func(owner, aggKey, findingType, evidence string, startsAt time.Time) *models.Event {
		ev := mkEvent(incidentNS, owner, aggKey, startsAt)
		_, err := tx.ExecContext(ctx,
			`UPDATE events SET finding_type = $2, evidences = $3::jsonb WHERE id = $1`,
			ev.Id, findingType, evidence)
		require.NoError(t, err)
		ev.FindingType = strPtr(findingType)
		var j models.Json
		require.NoError(t, j.Scan([]uint8(evidence)))
		ev.Evidences = &j
		return ev
	}

	// Placed by its own stored map, with nothing connected alerting: no group.
	placedMap := `[{"type": "knowledge_graph", "nodes": [
	  {"id": "n1", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "search", "namespace": "` + incidentNS + `"}},
	  {"id": "n2", "node_type": "Workload", "properties": {"kind": "Deployment", "name": "search-db", "namespace": "` + incidentNS + `"}}
	], "edges": [
	  {"relationship_type": "CALLS", "source_node_id": "n1", "dest_node_id": "n2"}
	]}]`
	placed := mkWith("search", "HighLatency", "issue", placedMap, anchor.Add(8*time.Minute))
	placed.SubjectType = strPtr("deployment")
	placed.SubjectOwnerKind = strPtr("Deployment")
	placedChild, err := attachSameSubjectIncident(ctx, tx, placed)
	require.NoError(t, err)
	assert.False(t, placedChild, "the graph placed it and nothing connected is alerting — timing must not group it")

	// A deploy event carries no map but is a Kubernetes object the graph knows.
	deploy := mkWith("relay", "ConfigurationChange/deployment", "configuration_change", "[]", anchor.Add(9*time.Minute))
	deployChild, err := attachSameSubjectIncident(ctx, tx, deploy)
	require.NoError(t, err)
	assert.False(t, deployChild, "a configuration change never groups on timing alone")
}

func TestSubjectKey_OwnerHashStripped(t *testing.T) {
	// Collectors disagree on the owner form for one workload: some report the
	// Deployment ("postgres"), some the ReplicaSet ("postgres-78d9cffd68").
	// Both must key to the same subject or same-incident attach misses
	// (observed live: a Pods-Restarting alert 6 minutes inside an open window
	// stayed unlinked because its owner carried the RS name).
	deployment := AlertIdentity{SubjectNamespace: "namespace-104a", SubjectOwner: "postgres"}
	replicaSet := AlertIdentity{SubjectNamespace: "namespace-104a", SubjectOwner: "postgres-78d9cffd68"}
	assert.Equal(t, SubjectKey(deployment), SubjectKey(replicaSet))
}
