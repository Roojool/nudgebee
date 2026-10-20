package aws

import (
	"fmt"
	"sort"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
)

// Which edges the account build asks each resource for.
//
// This file holds only the dispatch. Every resource registers the step that
// builds its own nodes' outgoing edges from its own file — vpc.go, sqs.go,
// rds.go and so on — so adding a resource never means editing a central
// switch. A node type no resource claims gets the default VPC edge, which is
// what the old switch's default arm did.

// edgeStep builds the outgoing edges of every node of one node type. newNodes
// are nodes the step synthesizes for its edges to land on (auto-scaling groups,
// Lambda container images, load-balancer backend pools); they join the graph
// but are deliberately not added to the lookup, as they were not before.
type edgeStep struct {
	NodeType core.NodeType
	Name     string
	Build    func(s *AWSSource, reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) (edges []*core.DbEdge, newNodes []*core.DbNode)
}

// nodeSetRebuild replaces the graph's nodes of one node type before any edge
// step runs. Only network interfaces need this: the DB rows are a stale
// superset of what the account actually has, so the ENI resolver returns the
// set to keep — including ENIs it synthesizes from rows the service filter
// dropped. Running every rebuild first makes what the other resources see
// deterministic; under the old switch it depended on Go's map iteration order.
type nodeSetRebuild struct {
	NodeType core.NodeType
	Name     string
	Rebuild  func(s *AWSSource, reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) (keep []*core.DbNode, edges []*core.DbEdge)
}

var (
	edgeSteps       = map[core.NodeType][]edgeStep{}
	nodeSetRebuilds = map[core.NodeType]nodeSetRebuild{}
)

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

// registerNodeSetRebuild declares that one resource decides which of its nodes
// stay in the graph. Call it from the resource file's init().
func registerNodeSetRebuild(rebuild nodeSetRebuild) {
	if rebuild.NodeType == "" || !rebuild.NodeType.IsKnown() {
		panic(fmt.Sprintf("node set rebuild %q declares unknown node type %q", rebuild.Name, rebuild.NodeType))
	}
	if rebuild.Name == "" || rebuild.Rebuild == nil {
		panic(fmt.Sprintf("node set rebuild for %q declares no name or no rebuilder", rebuild.NodeType))
	}
	if existing, ok := nodeSetRebuilds[rebuild.NodeType]; ok {
		panic(fmt.Sprintf("node set rebuild for %q is already registered (%q)", rebuild.NodeType, existing.Name))
	}
	nodeSetRebuilds[rebuild.NodeType] = rebuild
}

// buildEdges runs the registered steps over an account's nodes: the node-set
// rebuilds first, then one pass per node type present. Both phases run in a
// fixed order (node type, then step name) so a build is reproducible.
func (s *AWSSource) buildEdges(reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbNode, []*core.DbEdge) {
	edges := make([]*core.DbEdge, 0)

	for _, nodeType := range sortedNodeTypes(nodeSetRebuilds) {
		present, ok := lookup.ByNodeType[nodeType]
		if !ok {
			continue
		}
		rebuild := nodeSetRebuilds[nodeType]
		keep, rebuildEdges := rebuild.Rebuild(s, reqCtx, present, lookup, req)
		edges = append(edges, rebuildEdges...)

		kept := make([]*core.DbNode, 0, len(nodes))
		for _, node := range nodes {
			if node.NodeType != nodeType {
				kept = append(kept, node)
			}
		}
		nodes = append(kept, keep...)

		lookup.ByNodeType[nodeType] = keep
		for _, node := range keep {
			if resourceID, ok := node.Properties["resource_id"].(string); ok && resourceID != "" {
				lookup.ByResourceID[resourceID] = node
			}
		}
	}

	for _, nodeType := range sortedNodeTypes(lookup.ByNodeType) {
		nodeList := lookup.ByNodeType[nodeType]
		steps := edgeSteps[nodeType]
		if len(steps) == 0 {
			if _, rebuilt := nodeSetRebuilds[nodeType]; rebuilt {
				// The resource that owns the node set builds its edges there.
				continue
			}
			// No resource claims this node type's edges: give its nodes the
			// VPC they sit in, if their metadata names one. This is also what
			// resources with nothing else to say rely on (Bedrock catalog
			// entries, KMS keys, ECR repositories, hosted zones).
			edges = append(edges, s.createDefaultVPCEdges(nodeList, lookup, req)...)
			continue
		}
		for _, step := range sortedSteps(steps) {
			stepEdges, newNodes := step.Build(s, reqCtx, nodeList, lookup, req)
			edges = append(edges, stepEdges...)
			nodes = append(nodes, newNodes...)
		}
	}

	return nodes, edges
}

func sortedNodeTypes[V any](m map[core.NodeType]V) []core.NodeType {
	out := make([]core.NodeType, 0, len(m))
	for nodeType := range m {
		out = append(out, nodeType)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedSteps(steps []edgeStep) []edgeStep {
	out := make([]edgeStep, len(steps))
	copy(out, steps)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
