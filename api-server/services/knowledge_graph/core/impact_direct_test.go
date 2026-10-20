package core

import "testing"

// A workload's blast radius is its callers; a caller of a caller is a second
// application hop and must not survive the direct-only projection.
func TestRetainDirectDependents_DropsTransitiveCallers(t *testing.T) {
	seedID := "wl-seed"
	nodes := []*DbNode{
		newImpactTestNode(seedID, NodeTypeWorkload, "orders", "prod", "shop"),
		newImpactTestNode("wl-direct", NodeTypeWorkload, "checkout", "prod", "shop"),
		newImpactTestNode("wl-transitive", NodeTypeWorkload, "reporting", "prod", "shop"),
	}
	minDepth := map[string]int{seedID: 0, "wl-direct": 1, "wl-transitive": 2}
	edges := []*DbEdge{
		{SourceNodeID: "wl-direct", DestinationNodeID: seedID, RelationshipType: RelationshipCalls},
		{SourceNodeID: "wl-transitive", DestinationNodeID: "wl-direct", RelationshipType: RelationshipCalls},
	}

	got := summarizeImpact(seedID, NodeTypeWorkload, nodes, edges, minDepth, map[string]string{}, map[string]string{})
	if got.DependentCount != 2 || got.ProductionDependents != 2 {
		t.Fatalf("traversal should still find both callers, got count=%d prod=%d", got.DependentCount, got.ProductionDependents)
	}

	got.RetainDirectDependents()

	if got.DependentCount != 1 || len(got.Dependents) != 1 {
		t.Fatalf("DependentCount = %d, dependents = %+v, want only the direct caller", got.DependentCount, got.Dependents)
	}
	if got.Dependents[0].NodeID != "wl-direct" {
		t.Errorf("retained %q, want wl-direct", got.Dependents[0].NodeID)
	}
	if got.ProductionDependents != 1 {
		t.Errorf("ProductionDependents = %d, want 1 — the transitive caller's production tier must stop counting too", got.ProductionDependents)
	}
	if got.DependentsByType[NodeTypeWorkload] != 1 {
		t.Errorf("DependentsByType = %v, want one workload", got.DependentsByType)
	}
}

// The graph reaches a mounting workload through PV and PVC nodes. Those are
// infrastructure, not applications, so the workload is a DIRECT consumer of the
// volume however many edges the chain spans — trimming on raw hop count would
// erase the only dependent a delete-the-volume recommendation has.
func TestRetainDirectDependents_KeepsWorkloadBehindStorageChain(t *testing.T) {
	seedID := "vol-1"
	nodes := []*DbNode{
		newImpactTestNode(seedID, NodeTypeStorage, "pvc-0abc", "", ""),
		newImpactTestNode("pv-1", NodeTypePV, "pv-0abc", "", ""),
		newImpactTestNode("pvc-1", NodeTypePVC, "data-postgres-0", "", "db"),
		newImpactTestNode("wl-1", NodeTypeWorkload, "postgres", "prod", "db"),
	}
	minDepth := map[string]int{seedID: 0, "pv-1": 1, "pvc-1": 2, "wl-1": 3}
	edges := []*DbEdge{
		{SourceNodeID: "pv-1", DestinationNodeID: seedID, RelationshipType: RelationshipProvidesStorage},
		{SourceNodeID: "pvc-1", DestinationNodeID: "pv-1", RelationshipType: RelationshipIsBoundTo},
		{SourceNodeID: "wl-1", DestinationNodeID: "pvc-1", RelationshipType: RelationshipMounts},
	}

	got := summarizeImpact(seedID, NodeTypeStorage, nodes, edges, minDepth, map[string]string{}, map[string]string{})
	got.RetainDirectDependents()

	if got.DependentCount != 1 || len(got.Dependents) != 1 || got.Dependents[0].NodeID != "wl-1" {
		t.Fatalf("the mounting workload must survive, got count=%d deps=%+v", got.DependentCount, got.Dependents)
	}
	if got.Dependents[0].HopsAway != 1 {
		t.Errorf("HopsAway = %d, want 1 — a retained dependent reports dependency steps, not the edge count", got.Dependents[0].HopsAway)
	}
	if got.ProductionDependents != 1 {
		t.Errorf("ProductionDependents = %d, want 1", got.ProductionDependents)
	}
}

// A cloud instance reaches its pods through the k8s Node it runs, which is
// infrastructure — the pods stay direct.
func TestRetainDirectDependents_KeepsPodsBehindNode(t *testing.T) {
	seedID := "i-1"
	nodes := []*DbNode{
		newImpactTestNode(seedID, NodeTypeComputeInstance, "worker-1", "", ""),
		newImpactTestNode("node-1", NodeTypeNode, "gke-worker-1", "", ""),
		newImpactTestNode("pod-1", NodeTypePod, "checkout-abc", "prod", "shop"),
	}
	minDepth := map[string]int{seedID: 0, "node-1": 1, "pod-1": 2}
	edges := []*DbEdge{
		{SourceNodeID: "node-1", DestinationNodeID: seedID, RelationshipType: RelationshipRunsOn},
		{SourceNodeID: "pod-1", DestinationNodeID: "node-1", RelationshipType: RelationshipRunsOn},
	}

	got := summarizeImpact(seedID, NodeTypeComputeInstance, nodes, edges, minDepth, map[string]string{}, map[string]string{})
	got.RetainDirectDependents()

	if got.DependentCount != 1 || got.Dependents[0].NodeID != "pod-1" {
		t.Fatalf("the hosted pod must survive, got count=%d deps=%+v", got.DependentCount, got.Dependents)
	}
	if got.DependentsByType[NodeTypeNode] != 1 {
		t.Errorf("DependentsByType = %v, want the node still counted through the infrastructure list", got.DependentsByType)
	}
}

// The downstream list names a wider set than the dependents list — a database a
// workload calls is a dependency worth naming — so those types must count as an
// application hop, otherwise the seed's own backends would be trimmed away.
func TestRetainDirectDependents_TrimsDownstreamAtTheFirstDependency(t *testing.T) {
	seedID := "wl-seed"
	nodes := []*DbNode{
		newImpactTestNode(seedID, NodeTypeWorkload, "frontend", "prod", "shop"),
		newImpactTestNode("wl-1", NodeTypeWorkload, "product-catalog", "prod", "shop"),
		newImpactTestNode("db-1", NodeTypeDatabase, "catalog-db", "prod", ""),
	}
	minDepth := map[string]int{seedID: 0, "wl-1": 1, "db-1": 2}
	edges := []*DbEdge{
		{SourceNodeID: seedID, DestinationNodeID: "wl-1", RelationshipType: RelationshipCalls},
		{SourceNodeID: "wl-1", DestinationNodeID: "db-1", RelationshipType: RelationshipCalls},
	}

	summary := ImpactSummary{
		SeedNodeID:             seedID,
		DependentsByType:       map[NodeType]int{},
		Dependents:             []ImpactedService{},
		DownstreamDependencies: summarizeDownstream(seedID, nodes, edges, minDepth, map[string]string{}, map[string]string{}),
	}
	summary.DownstreamCount = len(summary.DownstreamDependencies)
	if summary.DownstreamCount != 2 {
		t.Fatalf("downstream traversal should find both, got %+v", summary.DownstreamDependencies)
	}

	summary.RetainDirectDependents()

	if summary.DownstreamCount != 1 || summary.DownstreamDependencies[0].NodeID != "wl-1" {
		t.Errorf("downstream = %+v, want only the workload the seed calls itself", summary.DownstreamDependencies)
	}
}

// Every consumer that keeps a prefix of a dependency list — the 15-entry
// downstream cap, the 50 the recommendation layer persists — treats the head of
// the list as the most relevant slice. A dependent that relies on the resource
// directly must therefore outrank a transitive one even when the graph reaches
// it through more edges, or the cap drops it before the direct-only projection
// ever sees it.
func TestSortImpactedServices_DirectOutranksFewerEdges(t *testing.T) {
	deps := []ImpactedService{
		{Name: "reporting", NodeType: NodeTypeWorkload, HopsAway: 2, AppHopsAway: 2},
		{Name: "postgres", NodeType: NodeTypeWorkload, HopsAway: 3, AppHopsAway: 1},
		{Name: "checkout", NodeType: NodeTypeWorkload, HopsAway: 1, AppHopsAway: 1},
	}
	sortImpactedServices(deps)

	wantOrder := []string{"checkout", "postgres", "reporting"}
	for i, name := range wantOrder {
		if deps[i].Name != name {
			t.Fatalf("position %d = %q, want %q — application hops order the list, edge count only breaks ties (full: %+v)", i, deps[i].Name, name, deps)
		}
	}
}
