package resourcepatch

import (
	"errors"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// updateWorkload refreshes a stored Kubernetes workload node and its own edges,
// or tombstones it when the workload is gone from the inventory.
func (a *Applier) updateWorkload(tenantID string, ref Ref, stored *core.StoredNode) Result {
	workloadID, _ := stored.Properties["nb_resource_id"].(string)
	if workloadID == "" {
		return rebuild("k8s node carries no workload id")
	}
	patch, err := a.k8s.BuildWorkload(tenantID, ref.AccountID, workloadID)
	if errors.Is(err, sources.ErrNotInInventory) {
		return a.tombstone(tenantID, stored)
	}
	if err != nil {
		return rebuild("workload build failed: " + err.Error())
	}
	if !patch.Patchable {
		return rebuild("workload kind " + patch.Kind + " is shaped by relay data")
	}

	node, res, ok := matchBuilt(stored, []*core.DbNode{patch.Node})
	if !ok {
		return res
	}
	contextNodes, err := a.loadContext(tenantID, ref.AccountID, []core.NodeType{core.NodeTypeNamespace})
	if err != nil {
		return rebuild("context read failed: " + err.Error())
	}
	if !containsNode(contextNodes, patch.Namespace.ID) {
		return rebuild("namespace not in the graph")
	}

	version, err := a.nextVersion(tenantID, stored.LastSyncVersion)
	if err != nil {
		return rebuild("sync version read failed: " + err.Error())
	}
	merged := mergeNode(stored.Properties, node, tenantID, ref.AccountID)
	return a.saveNodeAndEdges(tenantID, merged, patch.NewNodes, contextNodes, patch.Edges, patch.Signature, version)
}

// applyKubernetesNode handles a ref for a Kubernetes node, which is addressed by
// name. Only removal is applied per resource; new nodes rebuild the account
// through the change detector.
func (a *Applier) applyKubernetesNode(tenantID string, ref Ref) Result {
	if a.k8s == nil {
		return rebuild("no k8s patch policy registered")
	}
	nodes, err := a.store.FindActiveNodesByName(tenantID, ref.AccountID, a.k8s.Source(), a.k8s.NodeSpecificType(), ref.ProviderResourceID)
	if err != nil {
		return rebuild("node lookup failed: " + err.Error())
	}
	switch len(nodes) {
	case 0:
		return nothingToDo("no active node with the name")
	case 1:
	default:
		return rebuild("name matches several nodes")
	}
	present, err := a.k8s.HasActiveNode(tenantID, ref.AccountID, ref.ProviderResourceID)
	if err != nil {
		return rebuild("inventory read failed: " + err.Error())
	}
	if present {
		return nothingToDo("node is still in the inventory")
	}
	return a.tombstone(tenantID, nodes[0])
}
