package core

// DB-gated tests for the per-resource update primitives: provider-id lookup,
// the node+incident-edge tombstone, the signature-scoped outgoing-edge tombstone
// and the running-build check. Skips cleanly when no Postgres is reachable.

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"nudgebee/services/internal/testenv"
)

const resourcePatchTenant = "b2ca6e00-0000-4000-8000-000000000006"
const resourcePatchAccount = "b2ca6e00-0000-4000-8000-00000000cccc"
const resourcePatchOtherAccount = "b2ca6e00-0000-4000-8000-00000000dddd"

type resourcePatchFixture struct {
	svc      *Service
	repo     *ResourcePatchRepository
	instance *DbNode
	vpc      *DbNode
	subnet   *DbNode
	role     *DbNode
	caller   *DbNode
}

func newResourcePatchFixture(t *testing.T) *resourcePatchFixture {
	t.Helper()
	dbm := testenv.RequireMetastore(t)
	cleanup := func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`, resourcePatchTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_node WHERE tenant_id = $1::uuid`, resourcePatchTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_build_requests WHERE tenant_id = $1::uuid`, resourcePatchTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_tenant_filters WHERE tenant_id = $1::uuid`, resourcePatchTenant)
	}
	cleanup()
	t.Cleanup(cleanup)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(newTestRequestContext(), logger, dbm)

	node := func(nodeType NodeType, specificType, name string, props map[string]interface{}) *DbNode {
		key := BuildUniqueKey(CloudProviderAWS, resourcePatchAccount, "us-east-1", nodeType, "", name)
		props["specific_type"] = specificType
		props["name"] = name
		return NewNode(nodeType, key, props, resourcePatchTenant, resourcePatchAccount, "aws")
	}
	f := &resourcePatchFixture{
		svc:  svc,
		repo: NewResourcePatchRepository(dbm, logger),
		instance: node(NodeTypeComputeInstance, "EC2Instance", "web-1", map[string]interface{}{
			"resource_id":    "i-0abc",
			"nb_resource_id": "3f1c2d4e-0000-4000-8000-000000000001",
			"arn":            "arn:aws:ec2:us-east-1:111111111111:instance/i-0abc",
		}),
		vpc:    node(NodeTypeVPC, "VPC", "main-vpc", map[string]interface{}{"resource_id": "vpc-1"}),
		subnet: node(NodeTypeSubnet, "Subnet", "subnet-a", map[string]interface{}{"resource_id": "subnet-1"}),
		role:   node(NodeTypeServiceIdentity, "IAMRole", "web-role", map[string]interface{}{}),
		caller: node(NodeTypeComputeInstance, "EC2Instance", "caller", map[string]interface{}{"resource_id": "i-0caller"}),
	}
	if err := svc.SaveNodes([]*DbNode{f.instance, f.vpc, f.subnet, f.role, f.caller}, 1); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}
	all := []*DbNode{f.instance, f.vpc, f.subnet, f.role, f.caller}
	edges := []*DbEdge{
		NewEdge(f.instance.ID, f.vpc.ID, RelationshipHostedOn, map[string]interface{}{"connection_type": "vpc"}, resourcePatchTenant, resourcePatchAccount, "aws"),
		NewEdge(f.instance.ID, f.subnet.ID, RelationshipHostedOn, map[string]interface{}{"connection_type": "subnet"}, resourcePatchTenant, resourcePatchAccount, "aws"),
		NewEdge(f.instance.ID, f.role.ID, RelationshipRunsAs, map[string]interface{}{"connection_type": "instance_profile"}, resourcePatchTenant, resourcePatchAccount, "aws"),
		NewEdge(f.caller.ID, f.instance.ID, RelationshipCalls, map[string]interface{}{}, resourcePatchTenant, resourcePatchAccount, "traces"),
		NewEdge(f.caller.ID, f.vpc.ID, RelationshipHostedOn, map[string]interface{}{"connection_type": "vpc"}, resourcePatchTenant, resourcePatchAccount, "aws"),
	}
	if err := svc.SaveEdges(edges, all, 1); err != nil {
		t.Fatalf("seed edges: %v", err)
	}
	return f
}

func activeEdgeCount(t *testing.T, f *resourcePatchFixture, where string, args ...interface{}) int {
	t.Helper()
	var n int
	query := `SELECT COUNT(*) FROM knowledge_graph_edge WHERE tenant_id = $1 AND is_active = true AND ` + where
	if err := f.repo.dbManager.Db.Get(&n, query, append([]interface{}{resourcePatchTenant}, args...)...); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	return n
}

func TestFindActiveNodesByProviderID(t *testing.T) {
	f := newResourcePatchFixture(t)

	for name, id := range map[string]string{
		"resource_id":        "i-0abc",
		"nb_resource_id":     "3f1c2d4e-0000-4000-8000-000000000001",
		"arn, other casing":  "ARN:AWS:EC2:US-EAST-1:111111111111:INSTANCE/I-0ABC",
		"arn, stored casing": "arn:aws:ec2:us-east-1:111111111111:instance/i-0abc",
	} {
		got, err := f.repo.FindActiveNodesByProviderID(resourcePatchTenant, resourcePatchAccount, id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 1 || got[0].ID != f.instance.ID {
			t.Errorf("%s: got %d nodes, want the instance", name, len(got))
			continue
		}
		if got[0].LastSyncVersion != 1 || got[0].SpecificType != "EC2Instance" || got[0].UniqueKey != f.instance.UniqueKey {
			t.Errorf("%s: stored row not read back faithfully: %+v", name, got[0])
		}
	}

	// Scoped to the account: the same id in another account does not match.
	got, err := f.repo.FindActiveNodesByProviderID(resourcePatchTenant, resourcePatchOtherAccount, "i-0abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("lookup leaked across accounts: %d nodes", len(got))
	}
}

func TestTombstoneNodeWithIncidentEdges(t *testing.T) {
	f := newResourcePatchFixture(t)

	n, err := f.repo.TombstoneNodeWithIncidentEdges(resourcePatchTenant, f.instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	// vpc, subnet, runs_as outgoing + the behavioural CALLS edge incoming.
	if n != 4 {
		t.Errorf("tombstoned %d edges, want 4", n)
	}
	if got := activeEdgeCount(t, f, `(source_node_id = $2 OR destination_node_id = $2)`, f.instance.ID); got != 0 {
		t.Errorf("%d incident edges still active", got)
	}
	if got := activeEdgeCount(t, f, `source_node_id = $2`, f.caller.ID); got != 1 {
		t.Errorf("unrelated caller->vpc edge: %d active, want 1", got)
	}
	found, err := f.repo.FindActiveNodesByProviderID(resourcePatchTenant, resourcePatchAccount, "i-0abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Error("node still active after tombstone")
	}
}

func TestTombstoneOutgoingEdges_OnlySignatureBelowVersion(t *testing.T) {
	f := newResourcePatchFixture(t)

	// The re-emitted VPC edge is saved at the new version first, as the applier does.
	reemitted := NewEdge(f.instance.ID, f.vpc.ID, RelationshipHostedOn, map[string]interface{}{"connection_type": "vpc"}, resourcePatchTenant, resourcePatchAccount, "aws")
	if err := f.svc.SaveEdges([]*DbEdge{reemitted}, []*DbNode{f.instance, f.vpc}, 2); err != nil {
		t.Fatal(err)
	}

	sig := EdgeSignature{Source: "aws", Kinds: []EdgeKind{
		{Relationship: RelationshipHostedOn, ConnectionType: "vpc"},
		{Relationship: RelationshipHostedOn, ConnectionType: "subnet"},
	}}
	n, err := f.repo.TombstoneOutgoingEdges(resourcePatchTenant, f.instance.ID, sig, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("tombstoned %d edges, want 1 (the subnet edge)", n)
	}
	if got := activeEdgeCount(t, f, `source_node_id = $2 AND properties->>'connection_type' = 'vpc'`, f.instance.ID); got != 1 {
		t.Error("re-emitted vpc edge was tombstoned")
	}
	if got := activeEdgeCount(t, f, `source_node_id = $2 AND relationship_type = 'RUNS_AS'`, f.instance.ID); got != 1 {
		t.Error("RUNS_AS edge outside the signature was tombstoned")
	}
	if got := activeEdgeCount(t, f, `destination_node_id = $2`, f.instance.ID); got != 1 {
		t.Error("incoming CALLS edge was tombstoned")
	}
	if got := activeEdgeCount(t, f, `source_node_id = $2 AND relationship_type = 'HOSTED_ON'`, f.caller.ID); got != 1 {
		t.Error("another node's matching edge was tombstoned")
	}
}

func TestLoadActiveNodesByTypes(t *testing.T) {
	f := newResourcePatchFixture(t)
	nodes, err := f.repo.LoadActiveNodesByTypes(resourcePatchTenant, resourcePatchAccount, []NodeType{NodeTypeVPC, NodeTypeSubnet})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	for _, n := range nodes {
		if n.Properties["resource_id"] == nil {
			t.Errorf("node %s loaded without properties", n.ID)
		}
	}
}

// Only the empty case runs here: knowledge_graph_tenant_filters references
// tenant, which references users, so seeding filters needs a whole tenant chain.
func TestCurrentSyncVersion_NoFilters(t *testing.T) {
	f := newResourcePatchFixture(t)

	v, err := f.repo.CurrentSyncVersion(resourcePatchTenant)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("no filters: version %d, want 0", v)
	}
}

func TestExcludeBuilds_SeesRunningBuilds(t *testing.T) {
	f := newResourcePatchFixture(t)
	repo := NewBuildRequestRepository(f.repo.dbManager, slog.New(slog.NewTextHandler(io.Discard, nil)))

	insert := func(accounts string, startedAgo string) {
		t.Helper()
		if _, err := f.repo.dbManager.Exec(`
			INSERT INTO knowledge_graph_build_requests (tenant_id, account_ids, status, started_at, trigger)
			VALUES ($1, $2::jsonb, 'running', NOW() - $3::interval, 'test')
		`, resourcePatchTenant, accounts, startedAgo); err != nil {
			t.Fatalf("seed build request: %v", err)
		}
	}
	check := func(want bool, msg string) {
		t.Helper()
		var got bool
		err := repo.ExcludeBuilds(resourcePatchTenant, func(runningCovers func(string) (bool, error)) error {
			var err error
			got, err = runningCovers(resourcePatchAccount)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: got %v, want %v", msg, got, want)
		}
	}

	check(false, "nothing running")
	insert(`["`+resourcePatchOtherAccount+`"]`, "1 minute")
	check(false, "another account's build")
	insert(`[]`, "2 hours")
	check(false, "stale tenant-wide build")
	insert(`["`+resourcePatchOtherAccount+`","`+resourcePatchAccount+`"]`, "1 minute")
	check(true, "build scoped to this account")

	if _, err := f.repo.dbManager.Exec(`DELETE FROM knowledge_graph_build_requests WHERE tenant_id = $1`, resourcePatchTenant); err != nil {
		t.Fatal(err)
	}
	insert(`[]`, "1 minute")
	check(true, "live tenant-wide build")
}

// A build cannot be claimed while a per-resource update holds the tenant's
// build lock: the claim waits for the update to finish. This is what keeps a
// build from reading an account's inventory before a patch and saving that
// older state over it.
func TestExcludeBuilds_ClaimWaitsForPatch(t *testing.T) {
	f := newResourcePatchFixture(t)
	repo := NewBuildRequestRepository(f.repo.dbManager, slog.New(slog.NewTextHandler(io.Discard, nil)))

	entered := make(chan struct{})
	release := make(chan struct{})
	patchDone := make(chan error, 1)
	go func() {
		patchDone <- repo.ExcludeBuilds(resourcePatchTenant, func(func(string) (bool, error)) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	claimDone := make(chan error, 1)
	go func() {
		_, _, err := repo.TryClaim(resourcePatchTenant)
		claimDone <- err
	}()

	select {
	case err := <-claimDone:
		t.Fatalf("claim went ahead while a patch held the tenant's build lock (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	if err := <-patchDone; err != nil {
		t.Fatalf("patch: %v", err)
	}
	select {
	case err := <-claimDone:
		if err != nil {
			t.Fatalf("claim after the patch: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("claim never went ahead after the patch released the lock")
	}
}
