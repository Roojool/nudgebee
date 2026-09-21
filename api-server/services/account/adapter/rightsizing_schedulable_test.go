package adapter

import (
	"strings"
	"testing"
)

// The cluster these tests describe is the dev one the guard was built against:
// four nodes at ~14.4Gi allocatable, and a prometheus StatefulSet whose
// container holds a 2Gi request on ip-172-31-33-159. The committed figures are
// the measured ones, and they reconcile with the noisy_neighbours evidence
// collected independently for the same node.
func devCluster() (map[string]*nodeMemory, []containerRequest) {
	nodes := map[string]*nodeMemory{
		"node-a": {name: "node-a", allocatable: 14.45 * GiB},
		"node-b": {name: "node-b", allocatable: 14.45 * GiB},
		"node-c": {name: "node-c", allocatable: 14.28 * GiB},
		"node-d": {name: "node-d", allocatable: 14.45 * GiB},
	}
	requests := []containerRequest{
		// The workload under test: 2Gi prometheus + a small sidecar, on node-a.
		{node: "node-a", namespace: "nudgebee-agent", workload: "prometheus", container: "prometheus", bytes: 2 * GiB},
		{node: "node-a", namespace: "nudgebee-agent", workload: "prometheus", container: "config-reloader", bytes: 16 * MiB},
		// Everything else already promised on each node.
		{node: "node-a", namespace: "other", workload: "other-a", container: "app", bytes: 9.00 * GiB},
		{node: "node-b", namespace: "other", workload: "other-b", container: "app", bytes: 9.10 * GiB},
		{node: "node-c", namespace: "other", workload: "other-c", container: "app", bytes: 11.19 * GiB},
		{node: "node-d", namespace: "other", workload: "other-d", container: "app", bytes: 13.14 * GiB},
	}
	return nodes, requests
}

// The value the stored recommendation actually carries today. Nothing in the
// cluster can take it, so the apply must be refused rather than leaving the
// workload Pending with its old pod already gone.
func TestCheckMemoryHeadroom_RefusesValueThatFitsNowhere(t *testing.T) {
	nodes, requests := devCluster()
	err, known := checkMemoryHeadroom(nodes, requests, "nudgebee-agent", "prometheus",
		map[string]float64{"prometheus": 10 * GiB})
	if !known {
		t.Fatal("expected a verdict, got unknown")
	}
	if err == nil {
		t.Fatal("expected 10Gi to be refused on a cluster whose largest free block is ~5.3Gi")
	}
	// The message has to carry the numbers someone needs to act, not just a refusal.
	// node-a is the roomiest option precisely because the pod's own 2Gi comes back
	// when it is replaced there — 5.4Gi, against node-b's 5.35Gi.
	for _, want := range []string{"prometheus", "no node in this cluster has room", "node-a", "5.4Gi"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q, got: %v", want, err)
		}
	}
}

// The pod's own reservation is released when it is replaced, so on the node it
// already occupies that memory is available to the new spec. 5Gi fits there
// (3.43Gi free + 2Gi handed back) and must not be refused.
func TestCheckMemoryHeadroom_CountsBackTheWorkloadsOwnReservation(t *testing.T) {
	nodes, requests := devCluster()
	err, known := checkMemoryHeadroom(nodes, requests, "nudgebee-agent", "prometheus",
		map[string]float64{"prometheus": 5 * GiB})
	if !known {
		t.Fatal("expected a verdict, got unknown")
	}
	if err != nil {
		t.Fatalf("5Gi fits once the pod's own 2Gi is returned; should not be refused: %v", err)
	}
}

// The guard must not refuse a value merely because the pod's current node is
// full — the replacement goes through the scheduler against every node. Here
// node-a has no room but node-b does.
func TestCheckMemoryHeadroom_AllowsValueThatFitsAnotherNode(t *testing.T) {
	nodes := map[string]*nodeMemory{
		"node-a": {name: "node-a", allocatable: 8 * GiB},
		"node-b": {name: "node-b", allocatable: 64 * GiB},
	}
	requests := []containerRequest{
		{node: "node-a", namespace: "ns", workload: "app", container: "app", bytes: 1 * GiB},
		{node: "node-a", namespace: "other", workload: "filler", container: "app", bytes: 6 * GiB},
	}
	err, known := checkMemoryHeadroom(nodes, requests, "ns", "app",
		map[string]float64{"app": 16 * GiB})
	if !known {
		t.Fatal("expected a verdict, got unknown")
	}
	if err != nil {
		t.Fatalf("16Gi does not fit node-a but fits node-b; must not be refused: %v", err)
	}
}

// A pod is scheduled as a whole, so an unchanged sidecar still counts toward
// what has to fit. 5Gi for the main container plus a 10Gi sidecar fits nowhere
// even though neither value alone is impossible.
func TestCheckMemoryHeadroom_TotalsEveryContainerInThePod(t *testing.T) {
	nodes := map[string]*nodeMemory{
		"node-a": {name: "node-a", allocatable: 14 * GiB},
	}
	requests := []containerRequest{
		{node: "node-a", namespace: "ns", workload: "app", container: "app", bytes: 1 * GiB},
		{node: "node-a", namespace: "ns", workload: "app", container: "sidecar", bytes: 10 * GiB},
	}
	err, known := checkMemoryHeadroom(nodes, requests, "ns", "app",
		map[string]float64{"app": 5 * GiB})
	if !known {
		t.Fatal("expected a verdict, got unknown")
	}
	if err == nil {
		t.Fatal("5Gi + the pod's unchanged 10Gi sidecar exceeds a 14Gi node; expected a refusal")
	}
}

// A workload the inventory has never seen gives us no way to total the new pod,
// so the guard must return unknown and let the apply through rather than
// refusing on data it does not have.
func TestCheckMemoryHeadroom_UnknownWhenWorkloadNotInInventory(t *testing.T) {
	nodes, requests := devCluster()
	_, known := checkMemoryHeadroom(nodes, requests, "nudgebee-agent", "not-collected-yet",
		map[string]float64{"app": 10 * GiB})
	if known {
		t.Fatal("a workload absent from the pod inventory must read as unknown, not as a verdict")
	}
}

// Stale or partial inventory must never turn into a refusal. loadNodeMemory
// drops any node whose allocatable it does not know, so the decision can end up
// with no nodes at all — which has to read as unknown, not as "nothing fits".
func TestCheckMemoryHeadroom_EmptyNodeInventoryIsUnknown(t *testing.T) {
	err, known := checkMemoryHeadroom(map[string]*nodeMemory{}, nil, "ns", "app",
		map[string]float64{"app": 1 * GiB})
	if known {
		t.Fatal("an empty node inventory must read as unknown, not as a verdict")
	}
	if err != nil {
		t.Fatalf("an empty node inventory must not produce a refusal: %v", err)
	}
}

func TestRequestedMemoryByContainer(t *testing.T) {
	got := requestedMemoryByContainer([]any{
		map[string]any{"container_name": "app", "memory_request": "2Gi"},
		map[string]any{"container_name": "sidecar", "memory_request": "256Mi"},
		// No name, and a CPU-style quantity: both ignored rather than guessed at.
		map[string]any{"memory_request": "1Gi"},
		map[string]any{"container_name": "bad", "memory_request": "91m"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 parsed containers, got %d (%v)", len(got), got)
	}
	if got["app"] != 2*GiB {
		t.Errorf("app: want %v, got %v", float64(2*GiB), got["app"])
	}
	if got["sidecar"] != 256*MiB {
		t.Errorf("sidecar: want %v, got %v", float64(256*MiB), got["sidecar"])
	}
}
