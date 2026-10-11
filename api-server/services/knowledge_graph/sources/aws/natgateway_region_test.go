package aws

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// TestCreateNATGatewayEdgesWithoutRegionStillBuildsEdges guards the trap in the
// #36573 fix: the region is only needed for the *enrichment* fetch that fills in
// missing properties. Every edge below it is built from properties the node
// already carries, so a node with no resolvable region must still be linked —
// skipping the whole iteration would silently drop those edges.
func TestCreateNATGatewayEdgesWithoutRegionStillBuildsEdges(t *testing.T) {
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}

	vpcNode := &core.DbNode{
		NodeType:   core.NodeTypeVPC,
		Properties: map[string]interface{}{"resource_id": "vpc-1", "name": "vpc-1"},
	}
	subnetNode := &core.DbNode{
		NodeType:   core.NodeTypeSubnet,
		Properties: map[string]interface{}{"resource_id": "subnet-1", "name": "subnet-1"},
	}
	// No "region" property, and nothing else to derive one from — but vpc_id and
	// subnet_id are already present, so no AWS lookup is needed to link it.
	natNode := &core.DbNode{
		NodeType: core.NodeTypeNetworkGateway,
		Properties: map[string]interface{}{
			"resource_id": "nat-1",
			"name":        "nat-1",
			"vpc_id":      "vpc-1",
			"subnet_id":   "subnet-1",
		},
	}

	lookup := sources.NewNodeLookup([]*core.DbNode{vpcNode, subnetNode, natNode})
	req := &core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"}

	edges := src.createNATGatewayEdges(nil, []*core.DbNode{natNode}, lookup, req)

	if len(edges) != 2 {
		t.Fatalf("expected NAT gateway -> VPC and -> Subnet edges without a region, got %d", len(edges))
	}
}
