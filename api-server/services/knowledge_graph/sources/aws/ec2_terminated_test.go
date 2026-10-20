package aws

import (
	"encoding/json"
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// The defect this guards: AWS clears VpcId the moment an instance terminates,
// and the VPC is the hierarchy segment of a ComputeInstance's unique key. The
// terminated instance therefore keys differently from the running instance it
// used to be, so the account build inserted a SECOND node for one machine
// instead of updating the first, and the sweep tombstoned the original
// separately. On dev that produced 18 duplicated instances / 36 nodes.
func TestDropTerminatedInstances(t *testing.T) {
	tests := []struct {
		name  string
		state string
		kept  bool
	}{
		{"running instance is built", "running", true},
		{"stopped instance is built", "stopped", true},
		{"pending instance is built", "pending", true},
		{"terminated instance is not built", "terminated", false},
		{"shutting-down instance is not built", "shutting-down", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := computeNode("ec2", "EC2Instance", map[string]interface{}{
				"name":           "i-0abc",
				"resource_id":    "i-0abc",
				"instance_state": tc.state,
			})
			got := dropTerminatedInstances([]*core.DbNode{node})
			if tc.kept && len(got) != 1 {
				t.Fatalf("%s instance was dropped; want it kept", tc.state)
			}
			if !tc.kept && len(got) != 0 {
				t.Fatalf("%s instance was kept; want it dropped", tc.state)
			}
		})
	}
}

// An instance SSM manages but EC2 describe does not report (hybrid and on-prem
// activations) carries no instance_state and has no EC2 row to be terminated,
// so it must keep its node rather than vanish on a missing property.
func TestDropTerminatedInstances_KeepsNodesWithoutState(t *testing.T) {
	ssm := computeNode("ssm", "SSMManagedInstance", map[string]interface{}{
		"name":        "mi-0abc123",
		"resource_id": "mi-0abc123",
	})
	if got := dropTerminatedInstances([]*core.DbNode{ssm}); len(got) != 1 {
		t.Fatalf("node without instance_state was dropped; want it kept")
	}
}

// The SSM row of a terminated instance must go too. It carries no
// instance_state and no VPC, so leaving it behind would put
// aws:acct:region:ComputeInstance::i-0abc in the graph — the same duplicate
// dropping the EC2 node was meant to prevent. Raised in review of #39117.
func TestDropTerminatedInstances_DropsSSMSiblingOfTerminatedInstance(t *testing.T) {
	ec2 := computeNode("ec2", "EC2Instance", map[string]interface{}{
		"name":           "app-server",
		"resource_id":    "i-0abc",
		"instance_state": "terminated",
	})
	ssm := computeNode("ssm", "SSMManagedInstance", map[string]interface{}{
		"name":        "i-0abc",
		"resource_id": "i-0abc", // same machine, no state and no VPC
	})
	// An unrelated SSM-managed machine that is still alive.
	other := computeNode("other", "SSMManagedInstance", map[string]interface{}{
		"name":        "mi-0live",
		"resource_id": "mi-0live",
	})

	got := dropTerminatedInstances([]*core.DbNode{ec2, ssm, other})
	if len(got) != 1 || got[0] != other {
		var left []string
		for _, n := range got {
			rid, _ := core.GetNodePropertyString(n, "resource_id")
			left = append(left, rid)
		}
		t.Fatalf("kept %v; want only the live SSM-only instance mi-0live "+
			"(the SSM node of a terminated instance keys with an empty VPC)", left)
	}
}

// The drop is scoped to ComputeInstance: another node type that happens to
// carry a terminal-looking state keeps its node.
func TestDropTerminatedInstances_LeavesOtherNodeTypes(t *testing.T) {
	volume := rdsTestNode("vol", core.NodeTypeStorage, map[string]interface{}{
		"name":           "vol-1",
		"instance_state": "terminated",
	})
	ec2 := computeNode("ec2", "EC2Instance", map[string]interface{}{
		"name":           "i-0abc",
		"resource_id":    "i-0abc",
		"instance_state": "running",
	})
	got := dropTerminatedInstances([]*core.DbNode{volume, ec2})
	if len(got) != 2 {
		t.Fatalf("dropped %d of 2 nodes; want both kept", 2-len(got))
	}
}

// The point of the drop, end to end: one instance must hold one unique key for
// its whole life. Before the fix the terminated row keyed itself differently
// (empty hierarchy) and so became a second node.
func TestTerminatedInstanceDoesNotReKeyItself(t *testing.T) {
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}

	instance := func(state, vpcID string) *core.DbNode {
		props := map[string]interface{}{
			"name":           "i-0abc",
			"resource_id":    "i-0abc",
			"region":         "us-east-1",
			"instance_state": state,
		}
		if vpcID != "" {
			props["vpc_id"] = vpcID
		}
		n := computeNode("ec2", "EC2Instance", props)
		n.CloudAccountID = "acct-1"
		return n
	}

	running := instance("running", "vpc-123")
	runningKey := src.GenerateUniqueKey(running)

	// Same machine after AWS terminated it and dropped VpcId from the row.
	terminated := instance("terminated", "")
	if key := src.GenerateUniqueKey(terminated); key == runningKey {
		t.Skip("keys already match; the hierarchy segment no longer depends on the VPC")
	}

	// Because the key would differ, the terminated instance must not be built at
	// all — otherwise it lands in the graph as a second node for one machine.
	if got := dropTerminatedInstances([]*core.DbNode{terminated}); len(got) != 0 {
		t.Fatalf("terminated instance with a re-keyed unique key was still built: %q vs %q",
			src.GenerateUniqueKey(got[0]), runningKey)
	}
}

// instanceRow is one EC2 describe row as the collector stores it: AWS reports
// VpcId only while the instance is alive.
func instanceRow(instanceID, state, vpcID string) sources.CloudResourceRow {
	meta := map[string]interface{}{
		"InstanceType": "t3.micro",
		"State":        map[string]interface{}{"Name": state},
	}
	if vpcID != "" {
		meta["VpcId"] = vpcID
	}
	metaJSON, _ := json.Marshal(meta)
	empty, _ := json.Marshal(map[string]interface{}{})
	return sources.CloudResourceRow{
		ID:            instanceID,
		ResourceID:    instanceID,
		Name:          instanceID,
		Type:          "compute-instance",
		ServiceName:   "AmazonEC2",
		Account:       "all-types-account",
		Tenant:        "all-types-tenant",
		CloudProvider: "AWS",
		Region:        "us-east-1",
		IsActive:      true,
		Meta:          metaJSON,
		Tags:          empty,
	}
}

// The account build must not emit a node for a terminated instance. This drives
// convertResourcesToGraph rather than dropTerminatedInstances directly, so that
// removing the call from the build is a failure and not just a silent revert.
func TestConvertResourcesToGraph_SkipsTerminatedInstances(t *testing.T) {
	defer disableCloudCLIForTest()()
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}
	h := NewAWSSourceTestHelper(src)
	req := &core.SourceBuildRequest{TenantID: "all-types-tenant", CloudAccountID: "all-types-account"}

	rows := []sources.CloudResourceRow{
		instanceRow("i-running", "running", "vpc-123"),
		instanceRow("i-terminated", "terminated", ""),  // AWS has dropped VpcId
		instanceRow("i-shutting", "shutting-down", ""), // ditto, mid-termination
	}
	nodes, _ := h.ConvertResourcesToGraph(allTypesReqCtx(), rows, req)

	built := map[string]bool{}
	for _, n := range nodes {
		if n.NodeType != core.NodeTypeComputeInstance {
			continue
		}
		name, _ := core.GetNodePropertyString(n, "name")
		built[name] = true
	}
	if !built["i-running"] {
		t.Error("running instance produced no ComputeInstance node")
	}
	for _, gone := range []string{"i-terminated", "i-shutting"} {
		if built[gone] {
			t.Errorf("%s produced a ComputeInstance node; a terminated instance re-keys itself "+
				"(no VpcId) and so lands in the graph as a second node for one machine", gone)
		}
	}
}
