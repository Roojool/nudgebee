package core

import (
	"reflect"
	"testing"
)

func TestSplitOwnNodes(t *testing.T) {
	ctxIDs := map[string]struct{}{"ctx-1": {}, "ctx-2": {}}

	nodes := []*DbNode{
		{ID: "own-1"},
		{ID: "ctx-1"},
		nil,
		{ID: "own-2"},
		{ID: "ctx-2"},
	}

	got := splitOwnNodes(nodes, ctxIDs)
	want := []string{"own-1", "own-2"}
	gotIDs := make([]string, 0, len(got))
	for _, n := range got {
		gotIDs = append(gotIDs, n.ID)
	}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("splitOwnNodes = %v, want %v", gotIDs, want)
	}
}

// A tenant-wide build has no context, so every node must survive — including the
// nil-safety path, which must not silently drop real nodes.
func TestSplitOwnNodes_NoContextReturnsEverything(t *testing.T) {
	nodes := []*DbNode{{ID: "a"}, {ID: "b"}}
	got := splitOwnNodes(nodes, nil)
	if len(got) != 2 {
		t.Fatalf("splitOwnNodes with no context returned %d nodes, want 2", len(got))
	}
}

func TestDropContextOnlyEdges(t *testing.T) {
	ctxIDs := map[string]struct{}{"ctx-1": {}, "ctx-2": {}}

	edges := []*DbEdge{
		{ID: "own-to-own", SourceNodeID: "own-1", DestinationNodeID: "own-2"},
		{ID: "own-to-ctx", SourceNodeID: "own-1", DestinationNodeID: "ctx-1"},
		{ID: "ctx-to-own", SourceNodeID: "ctx-1", DestinationNodeID: "own-1"},
		{ID: "ctx-to-ctx", SourceNodeID: "ctx-1", DestinationNodeID: "ctx-2"},
	}

	got := dropContextOnlyEdges(edges, ctxIDs)
	want := []string{"own-to-own", "own-to-ctx", "ctx-to-own"}
	gotIDs := make([]string, 0, len(got))
	for _, e := range got {
		gotIDs = append(gotIDs, e.ID)
	}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("dropContextOnlyEdges = %v, want %v", gotIDs, want)
	}
}

// The ran-set is what stops a partial build tombstoning edges whose producer
// never ran. Anything absent here is protected, so the test pins both halves:
// what must be present, and what must never be.
func TestCollectRanEdgeSources(t *testing.T) {
	response := &BuildResponse{
		AccountMetadata: []AccountGraphMetadata{
			{AccountID: "a", SourcesBuilt: []string{"aws"}},
			{AccountID: "b", SourcesBuilt: []string{"k8s", "aws"}},
		},
	}

	// ranEnrichers are enricher GetName() values, exactly as BuildGraphs records
	// them. They double as edge stamps only because every enricher stamps its
	// edges with its own name — the contract test in
	// test/e2e_enricher_edge_source_test.go enforces that against the real
	// enrichers. Passing the stamp here instead of the name is what once hid a
	// name/stamp mismatch, so these must stay the real registered names.
	got := collectRanEdgeSources(response, []string{"k8s_ec2", "aws_lb_k8s"})
	want := []string{"aws", "aws_lb_k8s", "cross_account", "k8s", "k8s_ec2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectRanEdgeSources = %v, want %v", got, want)
	}

	// Producers a partial build never runs must not appear — their edges would
	// otherwise be swept by a build that has no evidence about them.
	forbidden := []string{"identity", "ownership", "github", "gitlab", "pagerduty", "manual", "ebpf", "traces", "cloud"}
	for _, name := range got {
		for _, bad := range forbidden {
			if name == bad {
				t.Errorf("ran-set contains %q, which does not run in a partial build", bad)
			}
		}
	}
}

// An enricher that failed is not in ranEnrichers, so its edges stay protected.
func TestCollectRanEdgeSources_OmitsEnrichersThatDidNotRun(t *testing.T) {
	got := collectRanEdgeSources(&BuildResponse{
		AccountMetadata: []AccountGraphMetadata{{AccountID: "a", SourcesBuilt: []string{"aws"}}},
	}, nil)

	for _, name := range got {
		if name == "k8s_ec2" {
			t.Fatalf("ran-set contains an enricher that did not run: %v", got)
		}
	}
}

// The only reachable identity collision in a partial build: a cross-source
// enricher synthesizes a node for an account this build did not rebuild (e.g. the
// K8s Service behind an AWS load balancer), and it lands on the same ID as the
// context node already loaded for that account. The context row must win, so the
// node is not persisted by a build that has no authority over that account.
//
// A rebuilt account's own node cannot collide with a context node at all, because
// the UniqueKey and the node ID both embed the cloud account — this test pins
// that too, by asserting the in-scope node survives untouched.
func TestSplitOwnNodes_EnricherSynthesizedNodeFoldsOntoContextRow(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	const scopeAccount = "22222222-2222-4222-8222-222222222222"
	const otherAccount = "33333333-3333-4333-8333-333333333333"

	ownKey := BuildUniqueKey(CloudProviderAWS, scopeAccount, "us-east-1", NodeTypeComputeInstance, "", "i-1")
	own := NewNode(NodeTypeComputeInstance, ownKey,
		map[string]interface{}{"name": "i-1"}, tenant, scopeAccount, "aws")

	// The context row for the other account, as loadContextNodes would return it.
	contextKey := BuildUniqueKey(CloudProviderK8s, otherAccount, "", NodeTypeService, "ns", "svc")
	contextNode := NewNode(NodeTypeService, contextKey,
		map[string]interface{}{"name": "svc", "origin": "context"}, tenant, otherAccount, "k8s")

	// What aws_lb_pod_target synthesizes for that same service during this build.
	synthesized := NewNode(NodeTypeService, contextKey,
		map[string]interface{}{"name": "svc", "origin": "enricher"}, tenant, otherAccount, "cloud")

	if synthesized.ID != contextNode.ID {
		t.Fatalf("synthesized node id %s does not match the context row %s; this test no longer exercises the collision",
			synthesized.ID, contextNode.ID)
	}
	if own.ID == contextNode.ID {
		t.Fatal("an in-scope node shares an id with a context node from another account; the account is no longer part of node identity")
	}

	contextIDs := map[string]struct{}{contextNode.ID: {}}

	// Slice order mirrors BuildGraphs: Phase 1 own nodes, Phase 2.0 context,
	// Phase 2.1 enricher output.
	deduped, _ := DeduplicateNodesWithIDMapping([]*DbNode{own, contextNode, synthesized})
	persisted := splitOwnNodes(deduped, contextIDs)

	if len(persisted) != 1 {
		t.Fatalf("partial build would persist %d nodes, want 1 (only the rebuilt account's)", len(persisted))
	}
	if persisted[0].ID != own.ID {
		t.Errorf("persisted node id = %s, want the in-scope node %s", persisted[0].ID, own.ID)
	}
	if persisted[0].CloudAccountID != scopeAccount {
		t.Errorf("persisted node belongs to account %s, want the rebuilt account %s",
			persisted[0].CloudAccountID, scopeAccount)
	}
}
