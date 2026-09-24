package aws

import (
	"log/slog"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// AWS per-resource update policy.
//
// This file holds only the mechanics shared by every AWS resource type. What a
// given type allows (account-level, creatable, edge targets, edge signature) is
// declared in that type's own file through registerPatchSpec, next to the code
// that builds it: ec2.go, rds.go, sqs.go, s3.go, lambda.go and so on. A type
// with no declaration gets sources.PatchSpec's zero value: its properties can be
// refreshed from the inventory, nothing more.

// edgeBuilder rebuilds one node's own outgoing edges with the same function the
// account build calls. lookup indexes the node plus the context nodes its spec
// asks for. newNodes are nodes the edges need that the builder synthesizes.
type edgeBuilder func(s *AWSSource, node *core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) (edges []*core.DbEdge, newNodes []*core.DbNode)

var patchSpecs = sources.NewPatchSpecRegistry[edgeBuilder]()

// registerPatchSpec declares how one AWS resource type is patched. Call it from
// the resource file's init(). build is nil for a spec without edges.
func registerPatchSpec(spec sources.PatchSpec, build edgeBuilder) {
	patchSpecs.Register(spec, build, build != nil)
}

// registerEdgesBuiltElsewhere declares edges this file's pass emits out of
// other resources' nodes. Call it from the pass's own file.
func registerEdgesBuiltElsewhere(kinds ...core.EdgeKind) {
	patchSpecs.RegisterEdgesBuiltElsewhere(kinds...)
}

func init() {
	sources.RegisterCloudPatchPolicy("aws", func(logger *slog.Logger) sources.CloudPatchPolicy {
		return newPatchPolicy(logger)
	})
}

// patchPolicy implements sources.CloudPatchPolicy for AWS.
type patchPolicy struct {
	source *AWSSource
}

func newPatchPolicy(logger *slog.Logger) *patchPolicy {
	// Configured exactly as the source registry configures it for a graph build.
	source, _ := NewAWSSource(AWSSourceConfig{ServiceTypeFilter: DefaultServiceTypeFilter}, logger)
	return &patchPolicy{source: source}
}

func (p *patchPolicy) Source() string { return "aws" }

// IncludeForGraph mirrors fetchAWSResources' SQL filter plus shouldIncludeResource.
func (p *patchPolicy) IncludeForGraph(row *sources.CloudResourceRow) bool {
	return row.Status == "Active" && row.IsActive && p.source.shouldIncludeResource(row)
}

func (p *patchPolicy) Classify(row *sources.CloudResourceRow, req *core.SourceBuildRequest) (sources.ResourceKind, bool) {
	p.source.fillRowCache([]sources.CloudResourceRow{*row})
	node := p.source.createNodeFromResource(row, req)
	if node == nil {
		return sources.ResourceKind{}, false
	}
	return sources.KindOf(node), true
}

func (p *patchPolicy) KindOf(node *core.DbNode) sources.ResourceKind {
	return sources.KindOf(node)
}

func (p *patchPolicy) EdgesBuiltElsewhere() []core.EdgeKind {
	return patchSpecs.EdgesBuiltElsewhere()
}

func (p *patchPolicy) Spec(kind sources.ResourceKind) sources.PatchSpec {
	spec, _, _ := patchSpecs.Lookup(kind)
	return spec
}

// baseContext is what every AWS patch loads: VPCs, whose names are part of most
// unique keys (propagateVPCNamesToResources), and subnets, so a reference to a
// network the graph does not have yet is detected (unresolvedNetworkReferences).
var baseContext = []core.NodeType{core.NodeTypeVPC, core.NodeTypeSubnet}

func (p *patchPolicy) ContextFor(kind sources.ResourceKind) []core.NodeType {
	return sources.MergeNodeTypes(baseContext, p.Spec(kind).Context)
}

// BuildNodes replays convertResourcesToGraph for one resource: include filter,
// createNodeFromResource, the terminated-instance drop, the EC2/SSM collapse,
// and the VPC-name unique-key rewrite resolved against contextNodes. It also
// fills the row cache the edge builders read, so call it before OutgoingEdges
// for the same resource.
//
// A ref for an instance AWS has terminated therefore builds no node, and
// matchBuilt turns that into a rebuild — which tombstones the stored node.
func (p *patchPolicy) BuildNodes(rows []sources.CloudResourceRow, contextNodes []*core.DbNode, req *core.SourceBuildRequest) ([]*core.DbNode, []string) {
	p.source.fillRowCache(rows)

	nodes := make([]*core.DbNode, 0, len(rows))
	for i := range rows {
		if !p.source.shouldIncludeResource(&rows[i]) {
			continue
		}
		if node := p.source.createNodeFromResource(&rows[i], req); node != nil {
			nodes = append(nodes, node)
		}
	}
	nodes = dropTerminatedInstances(nodes)
	nodes = collapseComputeInstanceDuplicates(nodes)

	lookup := sources.NewNodeLookup(contextNodes)
	missing := unresolvedNetworkReferences(nodes, lookup)
	p.source.propagateVPCNamesToResources(nodes, lookup)
	return nodes, missing
}

func (p *patchPolicy) OutgoingEdges(node *core.DbNode, contextNodes []*core.DbNode, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode, core.EdgeSignature, bool) {
	spec, build, found := patchSpecs.Lookup(sources.KindOf(node))
	if !found || build == nil {
		return nil, nil, core.EdgeSignature{}, false
	}
	lookup := sources.NewNodeLookup(append([]*core.DbNode{node}, contextNodes...))
	edges, newNodes := build(p.source, node, lookup, req)
	return edges, newNodes, spec.Signature, true
}

// edgesFrom keeps the edges that start at node. Builders written for a whole
// account emit edges for every node they can see; a patch owns only its own.
func edgesFrom(node *core.DbNode, edges []*core.DbEdge) []*core.DbEdge {
	own := make([]*core.DbEdge, 0, len(edges))
	for _, e := range edges {
		if e.SourceNodeID == node.ID {
			own = append(own, e)
		}
	}
	return own
}

// signature builds an "aws"-source edge signature.
func signature(kinds ...core.EdgeKind) core.EdgeSignature {
	return core.EdgeSignature{Source: "aws", Kinds: kinds}
}

// edgeKind is a (relationship, connection_type) pair; an empty connection type
// matches every edge of the relationship.
func edgeKind(rel core.RelationshipType, connectionType string) core.EdgeKind {
	return core.EdgeKind{Relationship: rel, ConnectionType: connectionType}
}
