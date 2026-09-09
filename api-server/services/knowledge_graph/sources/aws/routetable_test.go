package aws

import (
	"encoding/json"
	"strings"
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// TestEnsureRouteTableNodesCarryRegion pins #36573 for route tables. `route-table`
// rows never reach the classify path, so ensureRouteTableNodes is the only producer
// of RouteTable nodes — it used to stamp SourceBuildRequest.Region, which is never
// assigned, so every RouteTable node in production carried region "" and a unique key
// with an empty Location segment (aws:<acct>::RouteTable:<vpc>:<name>).
func TestEnsureRouteTableNodesCarryRegion(t *testing.T) {
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}

	meta, err := json.Marshal(map[string]interface{}{
		"RouteTableId": "rtb-1",
		"VpcId":        "vpc-1",
		"OwnerId":      "123456789012",
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}

	src.metaByType = map[string][]sources.CloudResourceRow{
		"route-table": {{
			ResourceID: "rtb-1",
			Type:       "route-table",
			Region:     "ap-south-1",
			Meta:       meta,
		}},
	}

	req := &core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"}
	nodes, _ := src.ensureRouteTableNodes(nil, nil, nil, sources.NewNodeLookup(nil), req)

	if len(nodes) != 1 {
		t.Fatalf("expected 1 route table node, got %d", len(nodes))
	}
	node := nodes[0]

	if got, _ := core.GetNodePropertyString(node, "region"); got != "ap-south-1" {
		t.Errorf("region property = %q, want %q", got, "ap-south-1")
	}

	// The unique key is the production symptom: a blank Location segment leaves
	// disambiguation resting entirely on the VPC id.
	if !strings.Contains(node.UniqueKey, ":ap-south-1:") {
		t.Errorf("unique key missing region segment: %q", node.UniqueKey)
	}
}
