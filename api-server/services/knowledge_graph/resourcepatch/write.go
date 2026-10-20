package resourcepatch

import (
	"nudgebee/services/knowledge_graph/core"
)

// Graph writes shared by every kind of patch.

func (a *Applier) tombstone(tenantID string, stored *core.StoredNode) Result {
	edges, err := a.store.TombstoneNodeWithIncidentEdges(tenantID, stored.ID)
	if err != nil {
		return rebuild("tombstone failed: " + err.Error())
	}
	return applied("tombstoned node and %d edges", edges)
}

// nextVersion is the sync version a patch stamps: one ahead of the last completed
// build, so a build that claims the tenant between the running-build check and
// the save (it stamps current+1 and sweeps below it) cannot sweep the patched
// rows; and one ahead of the node itself, so a second patch before the next build
// still outranks the first.
func (a *Applier) nextVersion(tenantID string, nodeVersion int64) (int64, error) {
	current, err := a.store.CurrentSyncVersion(tenantID)
	if err != nil {
		return 0, err
	}
	if nodeVersion > current {
		current = nodeVersion
	}
	return current + 1, nil
}

func (a *Applier) saveNode(node *core.DbNode, version int64) Result {
	if err := a.store.SaveNodes([]*core.DbNode{node}, version); err != nil {
		return rebuild("node save failed: " + err.Error())
	}
	return applied("properties written")
}

// saveNodeAndEdges writes the node, the nodes synthesized for its edges, then the
// edges, and only then retires the signature edges left below the new version, so
// a still-valid edge is never briefly inactive.
func (a *Applier) saveNodeAndEdges(tenantID string, node *core.DbNode, newNodes, contextNodes []*core.DbNode, edges []*core.DbEdge, sig core.EdgeSignature, version int64) Result {
	toSave := append([]*core.DbNode{node}, newNodes...)
	if err := a.store.SaveNodes(toSave, version); err != nil {
		return rebuild("node save failed: " + err.Error())
	}
	endpoints := make([]*core.DbNode, 0, len(toSave)+len(contextNodes))
	endpoints = append(endpoints, toSave...)
	endpoints = append(endpoints, contextNodes...)
	if err := a.store.SaveEdges(edges, endpoints, version); err != nil {
		return rebuild("edge save failed: " + err.Error())
	}
	removed, err := a.store.TombstoneOutgoingEdges(tenantID, node.ID, sig, version)
	if err != nil {
		return rebuild("edge tombstone failed: " + err.Error())
	}
	return applied("properties and %d edges written, %d edges retired", len(edges), removed)
}

// loadContext loads the account's active nodes of the given types.
func (a *Applier) loadContext(tenantID, accountID string, types []core.NodeType) ([]*core.DbNode, error) {
	if len(types) == 0 {
		return nil, nil
	}
	return a.store.LoadActiveNodesByTypes(tenantID, accountID, types)
}

// withoutNode drops the stored copy of the node being patched from its context:
// edge builders must only see the freshly built node.
func withoutNode(nodes []*core.DbNode, nodeID string) []*core.DbNode {
	out := make([]*core.DbNode, 0, len(nodes))
	for _, n := range nodes {
		if n.ID != nodeID {
			out = append(out, n)
		}
	}
	return out
}

func containsNode(nodes []*core.DbNode, nodeID string) bool {
	for _, n := range nodes {
		if n.ID == nodeID {
			return true
		}
	}
	return false
}

// matchBuilt finds the built node that is the stored node. The id is derived
// from the unique key, which carries the node type, account, location, hierarchy
// and name, so an id match proves the build would write this very row; anything
// else (renamed, re-typed, re-parented, or merged into another row) needs the
// account build.
func matchBuilt(stored *core.StoredNode, built []*core.DbNode) (*core.DbNode, Result, bool) {
	if len(built) == 0 {
		return nil, rebuild("source builds no node from the inventory rows"), false
	}
	for _, n := range built {
		if n.ID != stored.ID {
			continue
		}
		if n.NodeType != stored.NodeType || n.SpecificType != stored.SpecificType {
			return nil, rebuild("node type changed"), false
		}
		return n, Result{}, true
	}
	return nil, rebuild("built node id differs from the stored node (renamed or re-keyed)"), false
}

// mergeNode overlays the freshly built properties on the stored ones. Built
// values win; keys only the stored row has survive, because cross-source
// enrichers and account-wide passes (VPC names, datastore facets, CLI-derived
// network ids) write them and a single-resource build cannot recompute them.
// Going through NewNode regenerates labels, query_attributes and
// ontology_attributes from the merged properties.
func mergeNode(storedProperties map[string]interface{}, built *core.DbNode, tenantID, accountID string) *core.DbNode {
	props := make(map[string]interface{}, len(storedProperties)+len(built.Properties))
	for k, v := range storedProperties {
		props[k] = v
	}
	for k, v := range built.Properties {
		props[k] = v
	}
	props["specific_type"] = built.SpecificType
	return core.NewNode(built.NodeType, built.UniqueKey, props, tenantID, accountID, built.Source)
}
