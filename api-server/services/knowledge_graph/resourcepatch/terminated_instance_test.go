package resourcepatch

import (
	"encoding/json"
	"testing"
)

// An update message for an instance AWS has terminated must never write a
// second node for that machine.
//
// The window this covers: AWS clears VpcId the moment an instance terminates,
// and the VPC is the hierarchy segment of a ComputeInstance's unique key, so
// the row now keys differently from the node the graph already holds. The
// account build used to insert that re-keyed node as a new one; the patch path
// must not repeat it, whichever way the row arrives.
func TestUpdateTerminatedInstance_WritesNoSecondNode(t *testing.T) {
	st := awsFixture(t)
	stored := st.node(t, "i-0aaa")

	// Same row after termination: AWS has dropped VpcId and SubnetId.
	for i := range st.rows {
		if st.rows[i].ResourceID != "i-0aaa" {
			continue
		}
		st.rows[i].Meta, _ = json.Marshal(map[string]interface{}{
			"InstanceId": "i-0aaa",
			"State":      map[string]interface{}{"Name": "terminated"},
		})
	}

	res := newTestApplier(t, st, nil).Apply(testTenant, upsert("i-0aaa"))

	if res.Outcome == Applied {
		t.Errorf("terminated instance was applied (%q); it no longer carries the VPC its key is built from", res.Reason)
	}
	for _, n := range st.savedNodes {
		if n.ID != stored.ID {
			t.Errorf("wrote a node with a different id than the stored instance (%s vs %s): "+
				"that is the duplicate this guards against", n.ID, stored.ID)
		}
	}
	t.Logf("outcome=%v reason=%q saved_nodes=%d", res.Outcome, res.Reason, len(st.savedNodes))
}

// The same row while the instance is alive still applies, so the guard above is
// about the terminal state and not about EC2 refs in general.
func TestUpdateRunningInstance_StillApplies(t *testing.T) {
	st := awsFixture(t)
	for i := range st.rows {
		if st.rows[i].ResourceID != "i-0aaa" {
			continue
		}
		st.rows[i].Meta, _ = json.Marshal(map[string]interface{}{
			"InstanceId": "i-0aaa",
			"VpcId":      "vpc-1",
			"SubnetId":   "subnet-1",
			"State":      map[string]interface{}{"Name": "running"},
		})
	}

	res := newTestApplier(t, st, nil).Apply(testTenant, upsert("i-0aaa"))
	if res.Outcome != Applied {
		t.Fatalf("running instance was not applied: outcome=%v reason=%q", res.Outcome, res.Reason)
	}
}
