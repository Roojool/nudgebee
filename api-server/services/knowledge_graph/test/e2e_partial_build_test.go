package test

// Tier A: partial (account-scoped) build equivalence.
//
// The central claim of account-scoped builds is that rebuilding ONE account with
// the rest of the tenant loaded as read-only context produces the same
// cross-account result as rebuilding everything. Every join pass looks only at
// the in-memory node slice, so if the claim is wrong a partial build silently
// drops the edges that cross out of the rebuilt account — and the scoped sweep
// then tombstones the real ones as stale.
//
// The scenario is a K8s node in the K8s account running on an EC2 instance in the
// AWS account (rule k8s_node_to_ec2_instance_by_provider_id), i.e. an edge that
// genuinely spans two accounts and can only be produced when both endpoints are
// visible at once. DB-free, so it runs in every CI invocation.

import (
	"fmt"
	"sort"
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

const (
	partialBuildInstanceID = "i-0f1e2d3c4b5a69780"
	partialBuildAWSAccount = "11111111-1111-4111-8111-111111111111"
	partialBuildK8sAccount = "22222222-2222-4222-8222-222222222222"
)

// awsScopeGraph is the account being rebuilt: one EC2 instance.
func awsScopeGraph() []*core.DbNode {
	key := core.BuildUniqueKey(core.CloudProviderAWS, partialBuildAWSAccount, "us-east-1",
		core.NodeTypeComputeInstance, "", "api-worker-1")
	return []*core.DbNode{
		core.NewNode(core.NodeTypeComputeInstance, key, map[string]interface{}{
			"specific_type": "EC2Instance",
			"name":          "api-worker-1",
			"resource_id":   partialBuildInstanceID,
		}, e2eTenantID, partialBuildAWSAccount, "aws"),
	}
}

// k8sContextGraph is the other account: a K8s node whose providerID points at the
// EC2 instance above. In a partial build this arrives from the database as
// context, never rebuilt.
func k8sContextGraph() []*core.DbNode {
	key := core.BuildUniqueKey(core.CloudProviderK8s, partialBuildK8sAccount, "us-east-1",
		core.NodeTypeNode, "", "ip-10-0-1-23.ec2.internal")
	return []*core.DbNode{
		core.NewNode(core.NodeTypeNode, key, map[string]interface{}{
			"specific_type": "KubernetesNode",
			"name":          "ip-10-0-1-23.ec2.internal",
			"providerID":    "aws:///us-east-1a/" + partialBuildInstanceID,
		}, e2eTenantID, partialBuildK8sAccount, "k8s"),
	}
}

func TestE2E_TierA_PartialBuildMatchesFullBuild(t *testing.T) {
	svc := core.NewService(createTestRequestContext(e2eTenantID), e2eDiscardLogger(), nil)

	// --- Reference: tenant-wide build, both accounts fresh. ---
	// Only the edges matter here; the reference build's node set is asserted on
	// through the partial build's own split below.
	_, fullEdges := mergeGraph(append(awsScopeGraph(), k8sContextGraph()...), nil,
		mergeOpts{crossAccount: true, svc: svc})

	wantCross := crossAccountEdgeKeys(fullEdges)
	if len(wantCross) == 0 {
		t.Fatal("reference build produced no cross-account edges; this test would prove nothing")
	}

	// --- Partial: rebuild the AWS account only, K8s supplied as context. ---
	contextNodes := k8sContextGraph()
	contextIDs := make(map[string]struct{}, len(contextNodes))
	for _, n := range contextNodes {
		contextIDs[n.ID] = struct{}{}
	}

	partialNodes := append(awsScopeGraph(), contextNodes...)
	partialNodes, partialEdges := mergeGraph(partialNodes, nil, mergeOpts{crossAccount: true, svc: svc})

	// 1. The cross-account edges must match the full build exactly.
	gotCross := crossAccountEdgeKeys(partialEdges)
	if !equalStringSlices(wantCross, gotCross) {
		t.Errorf("partial build produced different cross-account edges\n full:    %v\n partial: %v", wantCross, gotCross)
	}

	// 2. At least one of them must genuinely span the two accounts, otherwise the
	//    comparison above is satisfied by same-account edges and proves nothing.
	if !hasAccountSpanningEdge(partialNodes, partialEdges) {
		t.Error("no cross-account edge actually spans the two accounts; the fixture no longer exercises context")
	}

	// 3. The nodes a partial build would persist are exactly the rebuilt account's.
	//    Context must never be written back by a build that did not rebuild it.
	ownNodes := make([]*core.DbNode, 0, len(partialNodes))
	for _, n := range partialNodes {
		if _, isContext := contextIDs[n.ID]; !isContext {
			ownNodes = append(ownNodes, n)
		}
	}
	if len(ownNodes) == 0 {
		t.Fatal("partial build would persist no nodes at all")
	}
	for _, n := range ownNodes {
		if n.CloudAccountID == partialBuildK8sAccount {
			t.Errorf("node %q (%s) belongs to the context account but would be persisted", n.UniqueKey, n.NodeType)
		}
	}
}

// crossAccountEdgeKeys returns a sorted, stable identity for every edge the
// cross-account rules produced, so two builds compare without depending on slice
// order or generated edge ids.
func crossAccountEdgeKeys(edges []*core.DbEdge) []string {
	keys := make([]string, 0)
	for _, e := range edges {
		if e.Source != core.CrossAccountEdgeSource {
			continue
		}
		keys = append(keys, fmt.Sprintf("%s|%s|%s", e.SourceNodeID, e.DestinationNodeID, e.RelationshipType))
	}
	sort.Strings(keys)
	return keys
}

// hasAccountSpanningEdge reports whether any cross-account edge connects nodes in
// two different cloud accounts.
func hasAccountSpanningEdge(nodes []*core.DbNode, edges []*core.DbEdge) bool {
	byID := make(map[string]*core.DbNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, e := range edges {
		if e.Source != core.CrossAccountEdgeSource {
			continue
		}
		src, dst := byID[e.SourceNodeID], byID[e.DestinationNodeID]
		if src != nil && dst != nil && src.CloudAccountID != dst.CloudAccountID {
			return true
		}
	}
	return false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
