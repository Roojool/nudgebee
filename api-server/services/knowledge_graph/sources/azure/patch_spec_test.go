package azure

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

func TestPatchSpecsAreValid(t *testing.T) {
	for _, err := range sources.CheckPatchSpecs(patchSpecs.Specs(), nil) {
		t.Error(err)
	}
}

// Every node type ensureSubnetNodes, ensureNICNodes and ensureNSGNodes create must
// be declared account-level, and no Azure type may be created on its own (see
// patch.go for why).
func TestAccountLevelAndNoCreates(t *testing.T) {
	policy := newPatchPolicy(nil)
	for _, nodeType := range []core.NodeType{core.NodeTypeVPC, core.NodeTypeSubnet, core.NodeTypeNetworkInterface, core.NodeTypeSecurityGroup} {
		if !policy.Spec(sources.ResourceKind{NodeType: nodeType, SpecificType: string(nodeType)}).AccountLevel {
			t.Errorf("%s is created by an account-wide pass but is not declared account-level", nodeType)
		}
	}
	for _, spec := range patchSpecs.Specs() {
		if spec.Creatable {
			t.Errorf("Azure spec %v is creatable; the row-driven reference passes make single-row creates incomplete", spec.SpecificTypes)
		}
	}
}
