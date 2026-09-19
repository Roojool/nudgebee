package azure

import (
	"fmt"
	"sort"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// Which edges the account build asks each resource for.
//
// This file holds only the dispatch. Every resource registers the step that
// builds its own nodes' outgoing edges from its own file — networking.go,
// vm.go, aks.go, database.go — so adding a resource never means editing a
// central switch. A node type no resource claims gets the default VNet
// attachment, which is what the old switch's default arm did; a resource that
// deliberately has no edges of its own registers a step that emits none.
//
// The reference, role-assignment and app-reference passes are not steps: they
// read the account's rows rather than one node type, and run after this.

// edgeStep builds the outgoing edges of every node of one node type.
type edgeStep struct {
	NodeType core.NodeType
	Name     string
	Build    func(s *AzureSource, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) []*core.DbEdge
}

var edgeSteps = map[core.NodeType][]edgeStep{}

// registerEdgeStep declares one resource's edge builder. Call it from the
// resource file's init(). Several resources may share a node type; each
// declares its own step.
func registerEdgeStep(step edgeStep) {
	if step.NodeType == "" || !step.NodeType.IsKnown() {
		panic(fmt.Sprintf("edge step %q declares unknown node type %q", step.Name, step.NodeType))
	}
	if step.Name == "" || step.Build == nil {
		panic(fmt.Sprintf("edge step for %q declares no name or no builder", step.NodeType))
	}
	for _, existing := range edgeSteps[step.NodeType] {
		if existing.Name == step.Name {
			panic(fmt.Sprintf("edge step %q for %q is already registered", step.Name, step.NodeType))
		}
	}
	edgeSteps[step.NodeType] = append(edgeSteps[step.NodeType], step)
}

// buildEdges runs the registered steps over an account's nodes, one pass per
// node type present, in a fixed order (node type, then step name) so a build is
// reproducible.
func (s *AzureSource) buildEdges(nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) []*core.DbEdge {
	edges := make([]*core.DbEdge, 0)

	nodeTypes := make([]core.NodeType, 0, len(lookup.ByNodeType))
	for nodeType := range lookup.ByNodeType {
		nodeTypes = append(nodeTypes, nodeType)
	}
	sort.Slice(nodeTypes, func(i, j int) bool { return nodeTypes[i] < nodeTypes[j] })

	for _, nodeType := range nodeTypes {
		nodeList := lookup.ByNodeType[nodeType]
		steps := edgeSteps[nodeType]
		if len(steps) == 0 {
			// No resource claims this node type's edges: attach its nodes to
			// the VNet their metadata names, if any.
			edges = append(edges, s.createDefaultVNetEdges(nodeList, lookup, req)...)
			continue
		}
		ordered := make([]edgeStep, len(steps))
		copy(ordered, steps)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
		for _, step := range ordered {
			edges = append(edges, step.Build(s, nodeList, lookup, req)...)
		}
	}

	return edges
}
