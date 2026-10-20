package core

// DB-gated tests for the account-scoped (partial build) tombstone sweeps.
//
// The scenario is the one that must not regress: two cloud accounts plus a
// tenant-scoped layer, rebuilding ONE account. Account B must come out of it
// completely untouched, A's own stale rows must be tombstoned, a cross-account
// edge whose A endpoint disappeared must be tombstoned even though it is stamped
// with B's account, and the ownership/github layer must survive a build that
// never ran its producers.
//
// Skips cleanly when no Postgres is reachable.

import (
	"io"
	"log/slog"
	"testing"

	"nudgebee/services/internal/database"
	"nudgebee/services/internal/testenv"
)

const scopedSweepTenant = "b2ca6e00-0000-4000-8000-000000000004"
const scopedSweepAccountA = "b2ca6e00-0000-4000-8000-00000000aaaa"
const scopedSweepAccountB = "b2ca6e00-0000-4000-8000-00000000bbbb"

func TestMarkInactiveInfraEdgesScoped_TwoAccounts(t *testing.T) {
	dbm := testenv.RequireMetastore(t)
	ctx := newTestRequestContext()
	svc := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), dbm)
	t.Cleanup(func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`, scopedSweepTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_node WHERE tenant_id = $1::uuid`, scopedSweepTenant)
	})

	newAWSNode := func(account, name string) *DbNode {
		key := BuildUniqueKey(CloudProviderAWS, account, "us-east-1", NodeTypeComputeInstance, "", name)
		return NewNode(NodeTypeComputeInstance, key,
			map[string]interface{}{"specific_type": "EC2Instance", "name": name},
			scopedSweepTenant, account, "aws")
	}
	newK8sNode := func(account, name string) *DbNode {
		key := BuildUniqueKey(CloudProviderK8s, account, "us-east-1", NodeTypeNode, "", name)
		return NewNode(NodeTypeNode, key,
			map[string]interface{}{"specific_type": "KubernetesNode", "name": name},
			scopedSweepTenant, account, "k8s")
	}

	// Account A: one instance that survives the rebuild, one that vanishes.
	aLive := newAWSNode(scopedSweepAccountA, "a-ec2-live")
	aStale := newAWSNode(scopedSweepAccountA, "a-ec2-stale")
	// Account B: untouched by this build.
	bNode := newK8sNode(scopedSweepAccountB, "b-node")
	// Tenant-scoped layer: ownership-sourced, cloud_account_id = tenant id.
	groupKey := BuildUniqueKey(CloudProviderExternal, scopedSweepTenant, "", NodeTypeUserGroup, "", "scoped-group")
	group := NewNode(NodeTypeUserGroup, groupKey,
		map[string]interface{}{"specific_type": "NudgebeeGroup", "name": "scoped-group"},
		scopedSweepTenant, scopedSweepTenant, "ownership")

	const oldSyncVersion = 1
	if err := svc.SaveNodes([]*DbNode{aLive, aStale, bNode, group}, oldSyncVersion); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// A-internal infra edge onto the node that is about to go stale.
	aInternal := NewEdge(aLive.ID, aStale.ID, RelationshipRunsOn, nil, scopedSweepTenant, scopedSweepAccountA, "aws")
	// Cross-account edges: stamped with B (the destination's account is A here, but
	// cross_account_relationships stamps the forward edge with the TARGET account —
	// seed both shapes so the endpoint-based predicate is what is under test).
	crossToStale := NewEdge(bNode.ID, aStale.ID, RelationshipRunsOn, nil, scopedSweepTenant, scopedSweepAccountB, CrossAccountEdgeSource)
	crossToLive := NewEdge(bNode.ID, aLive.ID, RelationshipRunsOn, nil, scopedSweepTenant, scopedSweepAccountB, CrossAccountEdgeSource)
	// Ownership edge into account B — its producer does not run in a partial build.
	ownershipEdge := NewEdge(group.ID, bNode.ID, RelationshipOwns, nil, scopedSweepTenant, scopedSweepTenant, "ownership")

	allNodes := []*DbNode{aLive, aStale, bNode, group}
	if err := svc.SaveEdges([]*DbEdge{aInternal, crossToStale, crossToLive, ownershipEdge}, allNodes, oldSyncVersion); err != nil {
		t.Fatalf("seed edges: %v", err)
	}

	// Re-stamp only what a partial rebuild of account A would produce: the live
	// instance and the cross-account edge that still resolves.
	const newSyncVersion = 2
	if err := svc.SaveNodes([]*DbNode{aLive}, newSyncVersion); err != nil {
		t.Fatalf("restamp nodes: %v", err)
	}
	if err := svc.SaveEdges([]*DbEdge{crossToLive}, allNodes, newSyncVersion); err != nil {
		t.Fatalf("restamp edges: %v", err)
	}

	// Sweep exactly as BuildGraphs would for Partial=true, scoped to account A.
	ranSources := collectRanEdgeSources(&BuildResponse{
		AccountMetadata: []AccountGraphMetadata{{AccountID: scopedSweepAccountA, SourcesBuilt: []string{"aws"}}},
	}, nil)
	if _, err := svc.markInactiveNodes(ctx, scopedSweepTenant, []string{scopedSweepAccountA}, newSyncVersion, nil, nil, false); err != nil {
		t.Fatalf("markInactiveNodes: %v", err)
	}
	if _, err := svc.markInactiveInfraEdgesScoped(scopedSweepTenant, newSyncVersion, []string{scopedSweepAccountA}, ranSources); err != nil {
		t.Fatalf("markInactiveInfraEdgesScoped: %v", err)
	}

	nodeActive := activeByID(t, dbm, `SELECT id, is_active FROM knowledge_graph_node WHERE tenant_id = $1::uuid`)
	edgeActive := activeByID(t, dbm, `SELECT id, is_active FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`)

	checks := []struct {
		what string
		got  map[string]bool
		id   string
		want bool
	}{
		{"account A live instance", nodeActive, aLive.ID, true},
		{"account A stale instance", nodeActive, aStale.ID, false},
		{"account B node (out of scope)", nodeActive, bNode.ID, true},
		{"ownership group (producer did not run)", nodeActive, group.ID, true},

		{"A-internal edge to the vanished instance", edgeActive, aInternal.ID, false},
		{"cross-account edge to the vanished instance", edgeActive, crossToStale.ID, false},
		{"cross-account edge to the surviving instance", edgeActive, crossToLive.ID, true},
		{"ownership edge (producer did not run)", edgeActive, ownershipEdge.ID, true},
	}
	for _, c := range checks {
		isActive, ok := c.got[c.id]
		if !ok {
			t.Errorf("%s: row missing from database entirely", c.what)
			continue
		}
		if isActive != c.want {
			t.Errorf("%s: is_active = %v, want %v", c.what, isActive, c.want)
		}
	}
}

// A partial build must never tombstone an edge whose producer did not run, even
// when both of its endpoints are inside the rebuilt account. This is the guard
// that keeps manual dependencies and flow edges alive across account rebuilds.
func TestMarkInactiveInfraEdgesScoped_ProtectsEdgesFromProducersThatDidNotRun(t *testing.T) {
	dbm := testenv.RequireMetastore(t)
	ctx := newTestRequestContext()
	svc := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), dbm)
	t.Cleanup(func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`, scopedSweepTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_node WHERE tenant_id = $1::uuid`, scopedSweepTenant)
	})

	mk := func(name string) *DbNode {
		key := BuildUniqueKey(CloudProviderAWS, scopedSweepAccountA, "us-east-1", NodeTypeComputeInstance, "", name)
		return NewNode(NodeTypeComputeInstance, key,
			map[string]interface{}{"specific_type": "EC2Instance", "name": name},
			scopedSweepTenant, scopedSweepAccountA, "aws")
	}
	src, dst := mk("protected-src"), mk("protected-dst")

	const oldSyncVersion = 1
	if err := svc.SaveNodes([]*DbNode{src, dst}, oldSyncVersion); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}
	manualEdge := NewEdge(src.ID, dst.ID, RelationshipRunsOn, nil, scopedSweepTenant, scopedSweepAccountA, "manual")
	if err := svc.SaveEdges([]*DbEdge{manualEdge}, []*DbNode{src, dst}, oldSyncVersion); err != nil {
		t.Fatalf("seed edges: %v", err)
	}

	// Rebuild account A from the aws source alone; "manual" is not in the ran-set.
	const newSyncVersion = 2
	ranSources := collectRanEdgeSources(&BuildResponse{
		AccountMetadata: []AccountGraphMetadata{{AccountID: scopedSweepAccountA, SourcesBuilt: []string{"aws"}}},
	}, nil)
	if _, err := svc.markInactiveInfraEdgesScoped(scopedSweepTenant, newSyncVersion, []string{scopedSweepAccountA}, ranSources); err != nil {
		t.Fatalf("markInactiveInfraEdgesScoped: %v", err)
	}

	edgeActive := activeByID(t, dbm, `SELECT id, is_active FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`)
	if isActive, ok := edgeActive[manualEdge.ID]; !ok || !isActive {
		t.Errorf("manual edge was tombstoned by a build that never ran the manual source: is_active=%v ok=%v", isActive, ok)
	}
}

// activeByID runs an (id, is_active) query for the scoped-sweep tenant and folds
// the result into a lookup map.
func activeByID(t *testing.T, dbm *database.DatabaseManager, query string) map[string]bool {
	t.Helper()
	rows, err := dbm.Query(query, scopedSweepTenant)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]bool)
	for rows.Next() {
		var id string
		var isActive bool
		if err := rows.Scan(&id, &isActive); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = isActive
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

// The bug this pins: an account rebuild that ran the k8s_ec2 enricher must clean
// up that enricher's stale edge — a K8s node still linked to an EC2 instance
// that no longer exists. The enricher used to stamp "k8s_ec2_enricher" while the
// sweep looked for "k8s_ec2", so the stale link survived until the next hourly
// build. It must also leave alone an edge stamped "cloud" by the centralized
// external-service enricher, which a partial build never re-runs: "cloud" is
// deliberately not a stamp any enricher that runs in a partial build uses.
func TestMarkInactiveInfraEdgesScoped_SweepsStaleEnricherEdgesButNotSharedCloudStamp(t *testing.T) {
	dbm := testenv.RequireMetastore(t)
	ctx := newTestRequestContext()
	svc := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), dbm)
	t.Cleanup(func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`, scopedSweepTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_node WHERE tenant_id = $1::uuid`, scopedSweepTenant)
	})

	mk := func(nodeType NodeType, name string) *DbNode {
		key := BuildUniqueKey(CloudProviderAWS, scopedSweepAccountA, "us-east-1", nodeType, "", name)
		return NewNode(nodeType, key,
			map[string]interface{}{"name": name},
			scopedSweepTenant, scopedSweepAccountA, "aws")
	}
	k8sNode := mk(NodeTypeNode, "ip-10-0-1-5")
	terminated := mk(NodeTypeComputeInstance, "i-terminated")
	externalSvc := mk(NodeTypeExternalService, "db.example.internal")
	database := mk(NodeTypeDatabase, "orders-db")

	const oldSyncVersion = 1
	allNodes := []*DbNode{k8sNode, terminated, externalSvc, database}
	if err := svc.SaveNodes(allNodes, oldSyncVersion); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// Stamped with the enricher's registered name, as the enricher now emits it.
	staleEnricherEdge := NewEdge(k8sNode.ID, terminated.ID, RelationshipRunsOn, nil,
		scopedSweepTenant, scopedSweepAccountA, "k8s_ec2")
	// Structural (RESOLVES_TO) edge from the external-service enricher, which only
	// runs with flow sources and so never runs in a partial build.
	sharedCloudEdge := NewEdge(externalSvc.ID, database.ID, RelationshipResolvesTo, nil,
		scopedSweepTenant, scopedSweepAccountA, "cloud")
	if err := svc.SaveEdges([]*DbEdge{staleEnricherEdge, sharedCloudEdge}, allNodes, oldSyncVersion); err != nil {
		t.Fatalf("seed edges: %v", err)
	}

	// Partial rebuild of account A in which aws, k8s_ec2 and aws_lb_pod_target all
	// ran and re-emitted neither edge.
	const newSyncVersion = 2
	ranSources := collectRanEdgeSources(&BuildResponse{
		AccountMetadata: []AccountGraphMetadata{{AccountID: scopedSweepAccountA, SourcesBuilt: []string{"aws"}}},
	}, []string{"k8s_ec2", "aws_lb_pod_target"})
	if _, err := svc.markInactiveInfraEdgesScoped(scopedSweepTenant, newSyncVersion, []string{scopedSweepAccountA}, ranSources); err != nil {
		t.Fatalf("markInactiveInfraEdgesScoped: %v", err)
	}

	edgeActive := activeByID(t, dbm, `SELECT id, is_active FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`)
	if isActive, ok := edgeActive[staleEnricherEdge.ID]; !ok {
		t.Error("stale enricher edge missing from database entirely")
	} else if isActive {
		t.Error("stale k8s_ec2 edge to a terminated instance survived an account rebuild that ran k8s_ec2")
	}
	if isActive, ok := edgeActive[sharedCloudEdge.ID]; !ok || !isActive {
		t.Errorf("edge stamped \"cloud\" by a producer that did not run was tombstoned: is_active=%v ok=%v", isActive, ok)
	}
}
