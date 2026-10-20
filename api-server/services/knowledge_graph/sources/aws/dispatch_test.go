package aws

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
)

// The dispatch has to be reproducible: the old switch walked lookup.ByNodeType,
// a Go map, so which set of network interfaces the other resources saw depended
// on iteration order. These tests pin the ordering guarantees the registry adds.

func TestEdgeStepsAreOrdered(t *testing.T) {
	for nodeType, steps := range edgeSteps {
		sorted := sortedSteps(steps)
		for i := 1; i < len(sorted); i++ {
			if sorted[i-1].Name >= sorted[i].Name {
				t.Errorf("%s steps are not strictly ordered: %q then %q",
					nodeType, sorted[i-1].Name, sorted[i].Name)
			}
		}
	}
}

// TestNodeSetRebuildsRunBeforeEdgeSteps proves the phase order the resources
// rely on: by the time any edge step runs, every rebuilt node type in the
// lookup is already the set its owner kept.
func TestNodeSetRebuildsRunBeforeEdgeSteps(t *testing.T) {
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}

	const (
		rebuiltType = core.NodeTypeNetworkInterface
		steppedType = core.NodeTypeStorage
	)
	req := &core.SourceBuildRequest{TenantID: "dispatch-tenant", CloudAccountID: "dispatch-account"}
	keep := &core.DbNode{ID: "keep", NodeType: rebuiltType, Properties: map[string]interface{}{"resource_id": "eni-keep"}}
	drop := &core.DbNode{ID: "drop", NodeType: rebuiltType, Properties: map[string]interface{}{"resource_id": "eni-drop"}}
	storage := &core.DbNode{ID: "storage", NodeType: steppedType, Properties: map[string]interface{}{}}

	// Swap the real registries for this test's two entries.
	origSteps, origRebuilds := edgeSteps, nodeSetRebuilds
	defer func() { edgeSteps, nodeSetRebuilds = origSteps, origRebuilds }()

	var seenByStep []*core.DbNode
	edgeSteps = map[core.NodeType][]edgeStep{}
	nodeSetRebuilds = map[core.NodeType]nodeSetRebuild{}
	registerNodeSetRebuild(nodeSetRebuild{NodeType: rebuiltType, Name: "test",
		Rebuild: func(s *AWSSource, _ *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbNode, []*core.DbEdge) {
			return []*core.DbNode{keep}, nil
		}})
	registerEdgeStep(edgeStep{NodeType: steppedType, Name: "test",
		Build: func(s *AWSSource, _ *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
			seenByStep = lookup.ByNodeType[rebuiltType]
			return nil, nil
		}})

	nodes := []*core.DbNode{keep, drop, storage}
	lookup := sources.NewNodeLookup(nodes)
	nodes, _ = src.buildEdges(nil, nodes, lookup, req)

	if len(seenByStep) != 1 || seenByStep[0] != keep {
		t.Errorf("edge step saw %d network interfaces; want only the kept one", len(seenByStep))
	}
	for _, node := range nodes {
		if node == drop {
			t.Error("a node the rebuild dropped is still in the graph")
		}
	}
	if len(nodes) != 2 {
		t.Errorf("graph has %d nodes; want the kept interface and the storage node", len(nodes))
	}
}

// TestRebuiltTypesGetNoDefaultEdges: a node type whose owner rebuilds the node
// set and emits its edges there must not also get the catch-all VPC edge, which
// is what its dedicated switch case used to guarantee.
func TestRebuiltTypesGetNoDefaultEdges(t *testing.T) {
	for nodeType := range nodeSetRebuilds {
		if steps := edgeSteps[nodeType]; len(steps) > 0 {
			t.Errorf("%s has both a node-set rebuild and %d edge steps; the rebuild already emits its edges",
				nodeType, len(steps))
		}
	}
}
