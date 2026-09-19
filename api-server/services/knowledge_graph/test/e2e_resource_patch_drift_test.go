package test

// Drift test for per-resource knowledge-graph updates (resourcepatch).
//
// A per-resource patch rebuilds one node, and for types that declare an edge
// signature its outgoing edges, from that resource's inventory rows plus the
// account's existing nodes loaded from the graph. It is only correct while it
// produces exactly what the account build produces. This test builds the fixture
// accounts the full way, then replays every resource through the registered patch
// policies with the full build's nodes standing in for the graph.
//
// Update mode, for every resource whose type is not account-level:
//   - the rebuilt node has the full build's id (which pins type, account,
//     location, hierarchy and name through the unique key) and specific type;
//   - within the type's signature, the rebuilt edges equal the full build's.
//
// Create mode, for every resource whose type is declared creatable:
//   - building it with no stored node gives the same node;
//   - every edge the patch emits is one the full build emits;
//   - every edge the full build gives the node outside the signature is one a
//     global pass or another resource's builder emits (these arrive with the
//     next account build).
//
// Resource files declare specs; this test is what keeps those declarations true.
// No database is required.

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/knowledge_graph/sources/k8s"
)

type patchEdgeKey struct {
	rel            core.RelationshipType
	connectionType string
	src, dst       string
}

func (k patchEdgeKey) String() string {
	return fmt.Sprintf("%s|%s|%s->%s", k.rel, k.connectionType, k.src, k.dst)
}

func keyOf(e *core.DbEdge) patchEdgeKey {
	connectionType, _ := e.Properties["connection_type"].(string)
	return patchEdgeKey{rel: e.RelationshipType, connectionType: connectionType, src: e.SourceNodeID, dst: e.DestinationNodeID}
}

func inSignature(e *core.DbEdge, sig core.EdgeSignature) bool {
	if e.Source != sig.Source {
		return false
	}
	return matchesAnyKind(keyOf(e), sig.Kinds)
}

func matchesAnyKind(k patchEdgeKey, kinds []core.EdgeKind) bool {
	for _, kind := range kinds {
		if kind.Relationship == k.rel && (kind.ConnectionType == "" || kind.ConnectionType == k.connectionType) {
			return true
		}
	}
	return false
}

// edgeKeys returns the sorted keys of the edges from nodeID that keep says to.
func edgeKeys(edges []*core.DbEdge, nodeID string, keep func(*core.DbEdge) bool) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, e := range edges {
		if e.SourceNodeID != nodeID || !keep(e) {
			continue
		}
		key := keyOf(e).String()
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func nodesOfTypes(nodes []*core.DbNode, types []core.NodeType, excludeID string) []*core.DbNode {
	want := make(map[core.NodeType]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	out := make([]*core.DbNode, 0)
	for _, n := range nodes {
		if want[n.NodeType] && n.ID != excludeID {
			out = append(out, n)
		}
	}
	return out
}

// awsDriftFixture builds the AWS fixture account the full way.
func awsDriftFixture(t *testing.T) ([]sources.CloudResourceRow, []*core.DbNode, []*core.DbEdge, *core.SourceBuildRequest) {
	t.Helper()
	req := &core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eAWSAccount}
	rows := loadCloudResourceRows(t, filepath.Join("testdata", "e2e", "aws_all_edges", "input", "aws.json"))
	rows = append(rows, loadCloudResourceRows(t, filepath.Join("testdata", "e2e", "resource_patch", "aws_extra.json"))...)
	// The fixtures omit status; production rows reach the build through a
	// status = 'Active' filter, which the policy's IncludeForGraph re-applies.
	for i := range rows {
		if rows[i].Status == "" {
			rows[i].Status = "Active"
		}
	}
	nodes, edges := buildAWSGraph(t, rows, req)
	return rows, nodes, core.DeduplicateEdges(edges), req
}

// resourceRows returns the included rows of the resource a full-build node came from.
func resourceRows(policy sources.CloudPatchPolicy, rows []sources.CloudResourceRow, node *core.DbNode) []sources.CloudResourceRow {
	resourceID, _ := node.Properties["resource_id"].(string)
	rowID, _ := node.Properties["nb_resource_id"].(string)
	out := make([]sources.CloudResourceRow, 0)
	for i := range rows {
		r := rows[i]
		if (resourceID != "" && r.ResourceID == resourceID) || (rowID != "" && r.ID == rowID) {
			if policy.IncludeForGraph(&r) {
				out = append(out, r)
			}
		}
	}
	return out
}

func TestResourcePatchDrift_AWSUpdate(t *testing.T) {
	defer disableCloudCLI()()
	rows, fullNodes, fullEdges, req := awsDriftFixture(t)
	policy := sources.NewCloudPatchPolicies(e2eDiscardLogger())["aws"]

	checkedNodes, checkedEdges := 0, 0
	for _, full := range fullNodes {
		kind := policy.KindOf(full)
		if full.Source != policy.Source() || policy.Spec(kind).AccountLevel {
			continue
		}
		own := resourceRows(policy, rows, full)
		if len(own) == 0 {
			continue // synthesized, or excluded by the default service filter
		}
		context := nodesOfTypes(fullNodes, policy.ContextFor(kind), full.ID)
		built, missing := policy.BuildNodes(own, context, req)
		if len(missing) > 0 {
			t.Errorf("%s %q: references %v missing from a graph that has them", kind, full.Properties["name"], missing)
			continue
		}
		match := findByID(built, full.ID)
		if match == nil {
			if len(built) == 1 {
				t.Errorf("%s %q: key drift: patch built id %s, full build %s", kind, full.Properties["name"], built[0].ID, full.ID)
			}
			continue // several rows share the id and build several nodes: the patch rebuilds
		}
		checkedNodes++
		if match.NodeType != full.NodeType || match.SpecificType != full.SpecificType {
			t.Errorf("%s %q: type drift: patch built %s/%s", kind, full.Properties["name"], match.NodeType, match.SpecificType)
		}

		edges, _, sig, ok := policy.OutgoingEdges(match, context, req)
		if !ok {
			continue
		}
		checkedEdges++
		keep := func(e *core.DbEdge) bool { return inSignature(e, sig) }
		want := edgeKeys(fullEdges, full.ID, keep)
		got := edgeKeys(edges, full.ID, keep)
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%s %q: edge drift\n full build: %v\n patch:      %v", kind, full.Properties["name"], want, got)
		}
	}
	if checkedNodes == 0 || checkedEdges < 6 {
		t.Fatalf("fixture exercised too little: %d nodes, %d edge sets", checkedNodes, checkedEdges)
	}
	t.Logf("checked %d nodes, %d edge sets", checkedNodes, checkedEdges)
}

func TestResourcePatchDrift_AWSCreate(t *testing.T) {
	defer disableCloudCLI()()
	rows, fullNodes, fullEdges, req := awsDriftFixture(t)
	policy := sources.NewCloudPatchPolicies(e2eDiscardLogger())["aws"]

	fullEdgeSet := make(map[string]bool, len(fullEdges))
	for _, e := range fullEdges {
		fullEdgeSet[keyOf(e).String()] = true
	}

	checked := 0
	for _, full := range fullNodes {
		kind := policy.KindOf(full)
		if full.Source != policy.Source() || !policy.Spec(kind).Creatable {
			continue
		}
		own := resourceRows(policy, rows, full)
		if len(own) == 0 {
			continue
		}
		classified, builds := policy.Classify(&own[0], req)
		if !builds || classified != kind {
			t.Errorf("%s %q: Classify says %v (builds=%v)", kind, full.Properties["name"], classified, builds)
			continue
		}
		context := nodesOfTypes(fullNodes, policy.ContextFor(kind), full.ID)
		built, missing := policy.BuildNodes(own, context, req)
		if len(missing) > 0 || len(built) != 1 || built[0].ID != full.ID {
			t.Errorf("%s %q: create builds %d nodes (missing %v), want the full build's node", kind, full.Properties["name"], len(built), missing)
			continue
		}
		checked++

		edges, _, sig, _ := policy.OutgoingEdges(built[0], context, req)
		for _, e := range edges {
			if !fullEdgeSet[keyOf(e).String()] {
				t.Errorf("%s %q: create emits an edge the full build does not: %s", kind, full.Properties["name"], keyOf(e))
			}
		}
		outside := edgeKeys(fullEdges, full.ID, func(e *core.DbEdge) bool {
			return !inSignature(e, sig) && !matchesAnyKind(keyOf(e), policy.EdgesBuiltElsewhere())
		})
		if len(outside) > 0 {
			t.Errorf("%s %q is declared creatable, but the full build gives it edges its patch cannot produce: %v", kind, full.Properties["name"], outside)
		}
	}
	if checked < 8 {
		t.Fatalf("fixture exercised too little: %d creatable nodes", checked)
	}
	t.Logf("checked %d creatable nodes", checked)
}

func findByID(nodes []*core.DbNode, id string) *core.DbNode {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

func TestResourcePatchDrift_K8sWorkloads(t *testing.T) {
	req := &core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eK8sAccount}
	workloads := loadK8sWorkloads(t)
	fullNodes, fullEdges := buildK8sGraph(t, workloads, loadK8sNodes(t), req)

	byID := make(map[string]*core.DbNode, len(fullNodes))
	for _, n := range fullNodes {
		byID[n.ID] = n
	}
	source, err := k8s.NewK8sSource(k8s.K8sSourceConfig{}, e2eDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	helper := k8s.NewK8sSourceTestHelper(source)

	checked := 0
	for i := range workloads {
		row := &workloads[i]
		patch := helper.BuildWorkloadPatch(row, map[string]string{}, req)
		full, ok := byID[patch.Node.ID]
		if !ok {
			t.Errorf("%s %s/%s: patch node id not produced by the full build", row.Kind, row.Namespace, row.Name)
			continue
		}
		if full.NodeType != patch.Node.NodeType || full.SpecificType != patch.Node.SpecificType {
			t.Errorf("%s %s/%s: type drift", row.Kind, row.Namespace, row.Name)
		}
		if _, ok := byID[patch.Namespace.ID]; !ok {
			t.Errorf("%s %s/%s: namespace node id not produced by the full build", row.Kind, row.Namespace, row.Name)
		}
		// The harness does not run BuildGraph's container-image synthesis, so
		// USES_IMAGE is left out of the comparison; the patch calls the same
		// synthesis function on its one node.
		keep := func(e *core.DbEdge) bool {
			return inSignature(e, patch.Signature) && e.RelationshipType != core.RelationshipUsesImage
		}
		want := edgeKeys(fullEdges, full.ID, keep)
		got := edgeKeys(patch.Edges, full.ID, keep)
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%s %s/%s: edge drift\n full build: %v\n patch:      %v", row.Kind, row.Namespace, row.Name, want, got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("fixture has no workloads")
	}
	t.Logf("checked %d workloads", checked)
}
