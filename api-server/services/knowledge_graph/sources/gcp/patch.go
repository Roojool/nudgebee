package gcp

import (
	"log/slog"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// GCP per-resource update policy.
//
// This file holds only the mechanics shared by every GCP resource type. What a
// given type allows is declared in that type's own file through
// registerPatchSpec (vpc.go, loadbalancer.go, pubsub.go, ...). Every GCP edge
// builder scans the whole account and several read live gcloud data, so no GCP
// spec rewrites edges yet: GCP patches refresh properties, and creatable GCP
// types are node-only.

// edgeBuilder rebuilds one node's own outgoing edges. No GCP spec declares one
// yet; the type exists so a spec can, without changing the policy.
type edgeBuilder func(s *GCPSource, node *core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) (edges []*core.DbEdge, newNodes []*core.DbNode)

var patchSpecs = sources.NewPatchSpecRegistry[edgeBuilder]()

// registerPatchSpec declares how one GCP resource type is patched. Call it from
// the resource file's init(). build is nil for a spec without edges.
func registerPatchSpec(spec sources.PatchSpec, build edgeBuilder) {
	patchSpecs.Register(spec, build, build != nil)
}

func init() {
	sources.RegisterCloudPatchPolicy("gcp", func(logger *slog.Logger) sources.CloudPatchPolicy {
		return newPatchPolicy(logger)
	})
}

// patchPolicy implements sources.CloudPatchPolicy for GCP.
type patchPolicy struct {
	source *GCPSource
}

func newPatchPolicy(logger *slog.Logger) *patchPolicy {
	// Configured exactly as the source registry configures it for a graph build.
	source, _ := NewGCPSource(GCPSourceConfig{ServiceTypeFilter: GCPDefaultServiceTypeFilter}, logger)
	return &patchPolicy{source: source}
}

func (p *patchPolicy) Source() string { return "gcp" }

// IncludeForGraph mirrors fetchGCPResources' SQL filter, shouldIncludeResource
// and the suppression createNodeFromResource applies.
func (p *patchPolicy) IncludeForGraph(row *sources.CloudResourceRow) bool {
	return row.IsActive && p.source.shouldIncludeResource(row) && !shouldSuppressGCPResource(row)
}

func (p *patchPolicy) Classify(row *sources.CloudResourceRow, req *core.SourceBuildRequest) (sources.ResourceKind, bool) {
	node := p.source.createNodeFromResource(row, req)
	if node == nil {
		return sources.ResourceKind{}, false
	}
	return p.KindOf(node), true
}

// KindOf is the node's kind, except for load-balancer chain resources, which
// share the CloudResource catch-all with unrelated rows and are told apart by
// their row type (see loadBalancerChainKind).
func (p *patchPolicy) KindOf(node *core.DbNode) sources.ResourceKind {
	if kind, ok := loadBalancerChainKind(node); ok {
		return kind
	}
	return sources.KindOf(node)
}

func (p *patchPolicy) EdgesBuiltElsewhere() []core.EdgeKind {
	return patchSpecs.EdgesBuiltElsewhere()
}

func (p *patchPolicy) Spec(kind sources.ResourceKind) sources.PatchSpec {
	spec, _, _ := patchSpecs.Lookup(kind)
	return spec
}

// GCP unique keys carry no parent-network names, so a patch needs no context
// beyond what a spec's edges ask for.
func (p *patchPolicy) ContextFor(kind sources.ResourceKind) []core.NodeType {
	return p.Spec(kind).Context
}

// BuildNodes replays the first step of convertResourcesToGraph for one resource.
// GCP's ensure*/enrich* passes run over CLI data, so kinds they touch are
// declared account-level and never reach here for a write.
func (p *patchPolicy) BuildNodes(rows []sources.CloudResourceRow, _ []*core.DbNode, req *core.SourceBuildRequest) ([]*core.DbNode, []string) {
	nodes := make([]*core.DbNode, 0, len(rows))
	for i := range rows {
		if !p.source.shouldIncludeResource(&rows[i]) {
			continue
		}
		if node := p.source.createNodeFromResource(&rows[i], req); node != nil {
			nodes = append(nodes, node)
		}
	}
	return nodes, nil
}

func (p *patchPolicy) OutgoingEdges(node *core.DbNode, contextNodes []*core.DbNode, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode, core.EdgeSignature, bool) {
	spec, build, found := patchSpecs.Lookup(p.KindOf(node))
	if !found || build == nil {
		return nil, nil, core.EdgeSignature{}, false
	}
	lookup := sources.NewNodeLookup(append([]*core.DbNode{node}, contextNodes...))
	edges, newNodes := build(p.source, node, lookup, req)
	return edges, newNodes, spec.Signature, true
}
