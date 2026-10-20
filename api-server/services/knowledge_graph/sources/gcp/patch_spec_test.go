package gcp

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

func TestPatchSpecsAreValid(t *testing.T) {
	for _, err := range sources.CheckPatchSpecs(patchSpecs.Specs(), nil, gcpHealthCheckKind, gcpTargetProxyKind) {
		t.Error(err)
	}
}

// Every node type an ensureGCP* pass creates must be declared account-level.
func TestAccountWidePassTypesAreAccountLevel(t *testing.T) {
	policy := newPatchPolicy(nil)
	passCreated := []*core.DbNode{
		{NodeType: core.NodeTypeVPC, SpecificType: string(core.NodeTypeVPC)},
		{NodeType: core.NodeTypeSubnet, SpecificType: string(core.NodeTypeSubnet)},
		{NodeType: core.NodeTypeComputeInstancePool, SpecificType: string(core.NodeTypeComputeInstancePool)},
		{NodeType: core.NodeTypeLoadBalancer, SpecificType: string(core.NodeTypeLoadBalancer)},
		{NodeType: core.NodeTypeBackendPool, SpecificType: string(core.NodeTypeBackendPool)},
		{NodeType: core.NodeTypeRouteTable, SpecificType: string(core.NodeTypeRouteTable)},
		{NodeType: core.NodeTypeDNSZone, SpecificType: string(core.NodeTypeDNSZone)},
		{NodeType: core.NodeTypeCDN, SpecificType: string(core.NodeTypeCDN)},
		{NodeType: core.NodeTypeCloudResource, SpecificType: string(core.NodeTypeCloudResource), Properties: map[string]interface{}{"type": rowTypeHealthCheck}},
		{NodeType: core.NodeTypeCloudResource, SpecificType: string(core.NodeTypeCloudResource), Properties: map[string]interface{}{"type": rowTypeTargetHTTPSProxy}},
	}
	for _, node := range passCreated {
		if !policy.Spec(policy.KindOf(node)).AccountLevel {
			t.Errorf("%s (%v) is created by an ensureGCP* pass but is not declared account-level", node.NodeType, node.Properties["type"])
		}
	}
	// An ordinary catch-all row is not caught by the load-balancer rule.
	plain := &core.DbNode{NodeType: core.NodeTypeCloudResource, SpecificType: string(core.NodeTypeCloudResource), Properties: map[string]interface{}{"type": "something"}}
	if policy.Spec(policy.KindOf(plain)).AccountLevel {
		t.Error("an ordinary CloudResource row must not be account-level")
	}
}
