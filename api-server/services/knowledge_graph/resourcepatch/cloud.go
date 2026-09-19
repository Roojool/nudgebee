package resourcepatch

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// updateCloudResource refreshes a stored cloud node from its inventory rows, or
// tombstones it when the account build would no longer produce it.
func (a *Applier) updateCloudResource(tenantID string, ref Ref, policy sources.CloudPatchPolicy, stored *core.StoredNode) Result {
	resourceID, _ := stored.Properties["resource_id"].(string)
	rowID, _ := stored.Properties["nb_resource_id"].(string)
	rows, err := a.store.LoadCloudResourceRows(ref.AccountID, resourceID, rowID)
	if err != nil {
		return rebuild("inventory read failed: " + err.Error())
	}
	included := includedRows(policy, rows)
	if len(included) == 0 {
		return a.tombstone(tenantID, stored)
	}

	kind := policy.KindOf(stored.DbNode)
	if policy.Spec(kind).AccountLevel {
		return rebuild(kind.String() + " is shaped by an account-wide pass")
	}
	contextNodes, err := a.loadContext(tenantID, ref.AccountID, policy.ContextFor(kind))
	if err != nil {
		return rebuild("context read failed: " + err.Error())
	}
	contextNodes = withoutNode(contextNodes, stored.ID)

	req := buildRequest(tenantID, ref.AccountID)
	built, missing := policy.BuildNodes(included, contextNodes, req)
	if len(missing) > 0 {
		return rebuild("references network resources not in the graph")
	}
	node, res, ok := matchBuilt(stored, built)
	if !ok {
		return res
	}
	version, err := a.nextVersion(tenantID, stored.LastSyncVersion)
	if err != nil {
		return rebuild("sync version read failed: " + err.Error())
	}
	merged := mergeNode(stored.Properties, node, tenantID, ref.AccountID)
	return a.writeCloudNode(tenantID, policy, merged, node, contextNodes, version)
}

// createCloudResource adds a node for a resource the graph does not have yet,
// when its type is declared creatable. Every other new resource rebuilds its
// account.
func (a *Applier) createCloudResource(tenantID string, ref Ref) Result {
	rows, err := a.store.LoadCloudResourceRows(ref.AccountID, ref.ProviderResourceID, ref.ProviderResourceID)
	if err != nil {
		return rebuild("inventory read failed: " + err.Error())
	}
	if len(rows) == 0 {
		return rebuild("no node and no inventory row for the provider id")
	}
	policy, ok := a.clouds[sources.SourceForCloudProvider(rows[0].CloudProvider)]
	if !ok {
		return rebuild("new " + rows[0].CloudProvider + " resource needs the account build")
	}
	req := buildRequest(tenantID, ref.AccountID)
	if !policy.IncludeForGraph(&rows[0]) {
		return nothingToDo("resource is outside the graph's scope")
	}
	kind, builds := policy.Classify(&rows[0], req)
	if !builds {
		return rebuild("the source builds this resource's node in an account-wide pass")
	}

	// Load the resource's other rows, so rows the build merges into one node (an
	// EC2 instance and its Systems Manager row) are built together. A provider id
	// is only unique per type, so unrelated resources can share it — keep the rows
	// that describe the same kind of thing.
	included := []sources.CloudResourceRow{rows[0]}
	if rows[0].ResourceID != "" {
		siblings, err := a.store.LoadCloudResourceRows(ref.AccountID, rows[0].ResourceID, "")
		if err != nil {
			return rebuild("inventory read failed: " + err.Error())
		}
		included = sameKindRows(policy, siblings, kind.NodeType, req)
	}
	if spec := policy.Spec(kind); spec.AccountLevel || !spec.Creatable {
		return rebuild("new " + kind.String() + " needs the account build")
	}
	contextNodes, err := a.loadContext(tenantID, ref.AccountID, policy.ContextFor(kind))
	if err != nil {
		return rebuild("context read failed: " + err.Error())
	}
	built, missing := policy.BuildNodes(included, contextNodes, req)
	if len(missing) > 0 {
		return rebuild("references network resources not in the graph")
	}
	if len(built) != 1 {
		return rebuild("inventory rows build several nodes")
	}
	node := built[0]
	if policy.KindOf(node) != kind {
		return rebuild("rows merge into a " + policy.KindOf(node).String())
	}

	// A node with this key may already exist under another provider id (the
	// resource was replaced under the same name). Treat it as an update of it.
	existing, err := a.store.FindActiveNodeByID(tenantID, node.ID)
	if err != nil {
		return rebuild("node lookup failed: " + err.Error())
	}
	storedProperties, storedVersion := map[string]interface{}(nil), int64(0)
	if existing != nil {
		storedProperties, storedVersion = existing.Properties, existing.LastSyncVersion
	}
	contextNodes = withoutNode(contextNodes, node.ID)

	version, err := a.nextVersion(tenantID, storedVersion)
	if err != nil {
		return rebuild("sync version read failed: " + err.Error())
	}
	merged := mergeNode(storedProperties, node, tenantID, ref.AccountID)
	return a.writeCloudNode(tenantID, policy, merged, node, contextNodes, version)
}

// writeCloudNode saves a patched node and, when its type declares them, its
// rebuilt outgoing edges. built is the node as the source built it, which edge
// builders read; merged is what is persisted.
func (a *Applier) writeCloudNode(tenantID string, policy sources.CloudPatchPolicy, merged, built *core.DbNode, contextNodes []*core.DbNode, version int64) Result {
	edges, newNodes, sig, ok := policy.OutgoingEdges(built, contextNodes, buildRequest(tenantID, merged.CloudAccountID))
	if !ok {
		return a.saveNode(merged, version)
	}
	return a.saveNodeAndEdges(tenantID, merged, newNodes, contextNodes, edges, sig, version)
}

// sameKindRows keeps the included rows that build a node of the same node type.
// A cloud provider id is unique per resource type, so one id can name, say, an
// ECR repository, an EKS cluster and a queue; only rows of one kind can merge
// into one node.
func sameKindRows(policy sources.CloudPatchPolicy, rows []sources.CloudResourceRow, nodeType core.NodeType, req *core.SourceBuildRequest) []sources.CloudResourceRow {
	out := make([]sources.CloudResourceRow, 0, len(rows))
	for i := range rows {
		if !policy.IncludeForGraph(&rows[i]) {
			continue
		}
		if kind, builds := policy.Classify(&rows[i], req); builds && kind.NodeType == nodeType {
			out = append(out, rows[i])
		}
	}
	return out
}

// includedRows keeps the rows the account build reads.
func includedRows(policy sources.CloudPatchPolicy, rows []sources.CloudResourceRow) []sources.CloudResourceRow {
	included := make([]sources.CloudResourceRow, 0, len(rows))
	for i := range rows {
		if policy.IncludeForGraph(&rows[i]) {
			included = append(included, rows[i])
		}
	}
	return included
}

func buildRequest(tenantID, accountID string) *core.SourceBuildRequest {
	return &core.SourceBuildRequest{TenantID: tenantID, CloudAccountID: accountID}
}
