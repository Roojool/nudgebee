package core

// DB-gated regression for #38046: an edge row whose primary key does not match
// its own endpoints must not block SaveEdges.
//
// knowledge_graph_edge's PK used to be derived from three of the five columns the
// upsert arbitrates on, so a row could miss the ON CONFLICT arm and still collide
// on knowledge_graph_edge_pkey. Migration V864 created exactly that shape in
// production: it repointed source_node_id/destination_node_id without rewriting
// id. Because SaveEdges wraps every batch in one transaction, the resulting 23505
// aborted every edge for the tenant on every subsequent build — permanently,
// since nothing ever rewrites the row that squats the key.
//
// This test seeds a V864-shaped row and then saves the edge whose derived PK
// collides with it. Against the pre-fix code it fails with
// `duplicate key value violates unique constraint "knowledge_graph_edge_pkey"`.
// Skips cleanly when no Postgres is reachable.

import (
	"io"
	"log/slog"
	"testing"

	"nudgebee/services/internal/testenv"
)

const (
	saveEdgesPKTenant  = "b2ca6e00-0000-4000-8000-000000000038"
	saveEdgesPKAccount = "b2ca6e00-0000-4000-8000-0000000000e1"
)

func TestSaveEdges_PoisonedPrimaryKeyDoesNotBlockTheBatch(t *testing.T) {
	dbm := testenv.RequireMetastore(t)
	ctx := newTestRequestContext()
	svc := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), dbm)
	t.Cleanup(func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`, saveEdgesPKTenant)
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_node WHERE tenant_id = $1::uuid`, saveEdgesPKTenant)
	})

	newNode := func(name string) *DbNode {
		key := BuildUniqueKey(CloudProviderAWS, saveEdgesPKAccount, "us-east-1", NodeTypeComputeInstance, "", name)
		return NewNode(NodeTypeComputeInstance, key,
			map[string]interface{}{"specific_type": "EC2Instance", "name": name},
			saveEdgesPKTenant, saveEdgesPKAccount, "aws")
	}
	// nodeA is the endpoint the build still emits; nodeC is the fossil endpoint
	// V864 repointed the poisoned row to.
	nodeA, nodeB, nodeC := newNode("node-a"), newNode("node-b"), newNode("node-c")

	const syncVersion = 1
	if err := svc.SaveNodes([]*DbNode{nodeA, nodeB, nodeC}, syncVersion); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// The V864-shaped row: a legacy GenerateEdgeID key — the scheme in use when
	// V864 ran — hashing endpoints A→B, but stored on C→B because the migration
	// repointed the endpoints and left the id alone. This is the row that used to
	// block the batch, and the shape the disjoint-hash-space argument turns on:
	// the fix steps around it precisely because EdgeIDFor never lands on a legacy
	// key. RelationshipCalls is seeded by V577, so the relationship_type FK holds.
	poisonedID := GenerateEdgeID(nodeA.ID, nodeB.ID, RelationshipCalls)
	if _, err := dbm.Exec(`
		INSERT INTO knowledge_graph_edge
			(id, created_at, updated_at, source_node_id, destination_node_id,
			 relationship_type, properties, cloud_account_id, tenant_id,
			 level, is_active, last_sync_version)
		VALUES ($1::uuid, now(), now(), $2::uuid, $3::uuid, $4, '{}'::jsonb,
			$5::uuid, $6::uuid, 'Tenant', true, $7)`,
		poisonedID, nodeC.ID, nodeB.ID, string(RelationshipCalls),
		saveEdgesPKAccount, saveEdgesPKTenant, syncVersion); err != nil {
		t.Fatalf("seed poisoned edge: %v", err)
	}

	// The edge the build legitimately produces. Its natural key is absent from the
	// table, so the upsert cannot resolve the clash by updating — pre-fix it
	// inserts and dies on the PK.
	edge := NewEdge(nodeA.ID, nodeB.ID, RelationshipCalls, nil, saveEdgesPKTenant, saveEdgesPKAccount, "aws")
	if err := svc.SaveEdges([]*DbEdge{edge}, []*DbNode{nodeA, nodeB, nodeC}, syncVersion+1); err != nil {
		t.Fatalf("SaveEdges must not fail on a poisoned primary key: %v", err)
	}

	// Both rows survive: the poisoned one is left for the tombstone sweep to age
	// out, and the real edge is persisted alongside it.
	var total int
	if err := dbm.QueryRowAndScan(&total, `SELECT count(*) FROM knowledge_graph_edge WHERE tenant_id = $1::uuid`, saveEdgesPKTenant); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if total != 2 {
		t.Errorf("expected the poisoned row and the saved edge to coexist, got %d rows", total)
	}

	var saved int
	if err := dbm.QueryRowAndScan(&saved, `
		SELECT count(*) FROM knowledge_graph_edge
		WHERE tenant_id = $1::uuid AND source_node_id = $2::uuid AND destination_node_id = $3::uuid`,
		saveEdgesPKTenant, nodeA.ID, nodeB.ID); err != nil {
		t.Fatalf("count saved edge: %v", err)
	}
	if saved != 1 {
		t.Errorf("expected the A→B edge to be persisted, found %d", saved)
	}
}
