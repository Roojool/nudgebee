package aws

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

func TestPatchSpecsAreValid(t *testing.T) {
	for _, err := range sources.CheckPatchSpecs(patchSpecs.Specs(), patchSpecs.EdgesBuiltElsewhere()) {
		t.Error(err)
	}
}

// Every node type an account-wide pass in convertResourcesToGraph creates or
// re-keys must be declared account-level, under the name its synthesized nodes
// carry (the NodeType name, since they set no specific type).
func TestAccountWidePassTypesAreAccountLevel(t *testing.T) {
	passCreated := []core.NodeType{
		core.NodeTypeVPC,                 // ensureVPCNodes
		core.NodeTypeSubnet,              // ensureSubnetNodes
		core.NodeTypeRouteTable,          // ensureRouteTableNodes
		core.NodeTypeLoadBalancer,        // live target-group fetch reshapes them
		core.NodeTypeBackendPool,         // target groups
		core.NodeTypeServiceIdentity,     // buildServiceIdentityNodes
		core.NodeTypeComputeInstancePool, // createASGNodesAndEdges
		core.NodeTypeContainerImage,      // createLambdaImageNodesAndEdges
	}
	policy := newPatchPolicy(nil)
	for _, nodeType := range passCreated {
		kind := sources.ResourceKind{NodeType: nodeType, SpecificType: string(nodeType)}
		if !policy.Spec(kind).AccountLevel {
			t.Errorf("%s is created by an account-wide pass but is not declared account-level", nodeType)
		}
	}
	// ENIs carry their NodeType name as their specific type.
	if !policy.Spec(sources.ResourceKind{NodeType: core.NodeTypeNetworkInterface, SpecificType: "NetworkInterface"}).AccountLevel {
		t.Error("NetworkInterface is created by createENIEdges but is not declared account-level")
	}
}

func TestUndeclaredTypeGetsSafeDefault(t *testing.T) {
	spec := newPatchPolicy(nil).Spec(sources.ResourceKind{NodeType: core.NodeTypeCloudResource, SpecificType: "SomethingNew"})
	if spec.AccountLevel || spec.Creatable || spec.HasEdges() {
		t.Errorf("undeclared type got %+v, want the zero spec", spec)
	}
}

// TestEdgesBuiltElsewhereAreDeclared: the passes that emit edges out of other
// resources' nodes declare them, so no signature can contain one by accident.
func TestEdgesBuiltElsewhereAreDeclared(t *testing.T) {
	want := map[core.EdgeKind]bool{
		{Relationship: core.RelationshipIsEncryptedBy}:                                       true, // kms.go
		{Relationship: core.RelationshipEmitsLogsTo}:                                         true, // cloudwatch.go
		{Relationship: core.RelationshipPublishesTo, ConnectionType: "event_source_mapping"}: true, // lambda.go
		{Relationship: core.RelationshipManages}:                                             true, // cloudformation.go
	}
	for _, kind := range patchSpecs.EdgesBuiltElsewhere() {
		delete(want, kind)
	}
	for kind := range want {
		t.Errorf("%s/%s is built by an account-wide pass but no file declares it", kind.Relationship, kind.ConnectionType)
	}
}
