package flow_sources

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"nudgebee/services/knowledge_graph/core"
)

const testK8sAccount = "k8s-1"

func mkWorkload(id, namespace, kind, name string) *core.DbNode {
	n := mkNode(id, core.NodeTypeWorkload, name)
	n.CloudAccountID = testK8sAccount
	n.Properties["namespace"] = namespace
	n.Properties["kind"] = kind
	return n
}

func mkK8sNode(id, name, internalIP string) *core.DbNode {
	n := mkNode(id, core.NodeTypeNode, name)
	n.CloudAccountID = testK8sAccount
	n.Source = "k8s"
	n.Properties["internal_ip"] = internalIP
	return n
}

func podRow(ip, namespace, pod, createdByKind, createdByName string) map[string]string {
	return map[string]string{
		"pod_ip": ip, "namespace": namespace, "pod": pod,
		"created_by_kind": createdByKind, "created_by_name": createdByName,
	}
}

func TestResolveK8sIPsForAccount(t *testing.T) {
	services := mkWorkload("wl-services", "nudgebee", "Deployment", "services-server")
	ml := mkWorkload("wl-ml", "nudgebee", "Deployment", "ml-server")
	liveNode := mkK8sNode("node-live", "ip-10-1-0-5", "10.1.0.5")
	existing := []*core.DbNode{services, ml, liveNode}

	hostNet := podRow("10.1.0.9", "kube-system", "aws-node-x", "DaemonSet", "aws-node")
	hostNet["host_network"] = "true"
	pods := []map[string]string{
		// one owner → resolves to the Workload
		podRow("10.1.1.1", "nudgebee", "services-server-5c4f7b64c-x", "ReplicaSet", "services-server-5c4f7b64c"),
		// two pods of the same owner in the window → still one owner
		podRow("10.1.1.4", "nudgebee", "services-server-5c4f7b64c-y", "ReplicaSet", "services-server-5c4f7b64c"),
		podRow("10.1.1.4", "nudgebee", "services-server-84b684477c-z", "ReplicaSet", "services-server-84b684477c"),
		// IP reused by two different workloads in the window → ambiguous
		podRow("10.1.1.2", "nudgebee", "services-server-5c4f7b64c-w", "ReplicaSet", "services-server-5c4f7b64c"),
		podRow("10.1.1.2", "nudgebee", "ml-server-57db7bbb6b-v", "ReplicaSet", "ml-server-57db7bbb6b"),
		// owner the K8s source never emitted → not in graph
		podRow("10.1.1.3", "nudgebee", "rag-server-598bbbcb9c-u", "ReplicaSet", "rag-server-598bbbcb9c"),
		// pods on a node IP whose node is gone: never attributed to a pod
		hostNet,
		podRow("10.1.0.9", "kube-system", "kube-proxy-x", "DaemonSet", "kube-proxy"),
	}
	nodes := []map[string]string{{"internal_ip": "10.1.0.9", "node": "ip-10-1-0-9"}}

	got := resolveK8sIPsForAccount(
		[]string{"10.1.1.1", "10.1.1.4", "10.1.1.2", "10.1.1.3", "10.1.0.5", "10.1.0.9", "10.1.9.9"},
		testK8sAccount,
		NewK8sNodeIPResolver(existing),
		indexWorkloadsByOwner(existing, testK8sAccount),
		pods, nodes,
	)

	wantNode := map[string]struct{ id, tag string }{
		"10.1.1.1": {"wl-services", resolutionK8sPodIP},
		"10.1.1.4": {"wl-services", resolutionK8sPodIP},
		"10.1.0.5": {"node-live", resolutionK8sNodeIP},
	}
	for ip, want := range wantNode {
		r := got[ip]
		if r.node == nil || r.node.ID != want.id || r.tag != want.tag {
			t.Errorf("%s resolved to %+v, want node %s tag %s", ip, r, want.id, want.tag)
		}
	}
	wantReason := map[string]string{
		"10.1.1.2": reasonVPCIPAmbiguousOwner,
		"10.1.1.3": reasonVPCIPWorkloadNotInKG,
		"10.1.0.9": reasonVPCIPK8sNodeGone,
	}
	for ip, want := range wantReason {
		if r := got[ip]; r.node != nil || r.reason != want {
			t.Errorf("%s = %+v, want unresolved with reason %s", ip, r, want)
		}
	}
	if _, ok := got["10.1.9.9"]; ok {
		t.Errorf("10.1.9.9 has no pod or node data and should be absent, got %+v", got["10.1.9.9"])
	}
}

// The same namespace/kind/name in two clusters of one account is two owners:
// an IP both held in the window is ambiguous, not "workload not in graph".
func TestResolveK8sIPsForAccount_SameOwnerNameInTwoClustersIsAmbiguous(t *testing.T) {
	a := mkWorkload("wl-a", "kube-system", "Deployment", "coredns")
	a.Properties["cluster"] = "cluster-a"
	b := mkWorkload("wl-b", "kube-system", "Deployment", "coredns")
	b.Properties["cluster"] = "cluster-b"
	existing := []*core.DbNode{a, b}
	rowA := podRow("10.5.5.5", "kube-system", "coredns-5c4f7b64c-a", "ReplicaSet", "coredns-5c4f7b64c")
	rowA["k8s_cluster"] = "cluster-a"
	rowB := podRow("10.5.5.5", "kube-system", "coredns-84b684477c-b", "ReplicaSet", "coredns-84b684477c")
	rowB["k8s_cluster"] = "cluster-b"

	got := resolveK8sIPsForAccount([]string{"10.5.5.5"}, testK8sAccount, NewK8sNodeIPResolver(existing),
		indexWorkloadsByOwner(existing, testK8sAccount), []map[string]string{rowA, rowB}, nil)
	if r := got["10.5.5.5"]; r.node != nil || r.reason != reasonVPCIPAmbiguousOwner {
		t.Errorf("10.5.5.5 = %+v, want unresolved with reason %s", r, reasonVPCIPAmbiguousOwner)
	}
}

// With several K8s accounts mapped to one AWS account, the account tried first
// must not depend on the caller's (map-derived) order.
func TestResolveFlowIPsViaK8s_AccountOrderIsDeterministic(t *testing.T) {
	wlA := mkWorkload("wl-a", "ns", "Deployment", "svc-a")
	wlA.CloudAccountID = "k8s-a"
	wlB := mkWorkload("wl-b", "ns", "Deployment", "svc-b")
	wlB.CloudAccountID = "k8s-b"
	existing := []*core.DbNode{wlA, wlB}
	fetch := func(acct string, _ []string, _, _ time.Time) ([]map[string]string, []map[string]string, error) {
		name := map[string]string{"k8s-a": "svc-a-5c4f7b64c", "k8s-b": "svc-b-5c4f7b64c"}[acct]
		return []map[string]string{podRow("10.6.6.6", "ns", name+"-x", "ReplicaSet", name)}, nil, nil
	}
	end := time.Now()
	for _, order := range [][]string{{"k8s-a", "k8s-b"}, {"k8s-b", "k8s-a"}} {
		got, _ := resolveFlowIPsViaK8s([]string{"10.6.6.6"}, order, existing, NewK8sNodeIPResolver(existing), fetch, end.Add(-time.Hour), end)
		if r := got["10.6.6.6"]; r.node == nil || r.node.ID != "wl-a" {
			t.Errorf("order %v: 10.6.6.6 = %+v, want wl-a (k8s-a sorts first)", order, r)
		}
	}
}

// A metrics failure must not cost the lookups that don't need metrics: an
// active K8s Node still resolves from the graph alone.
func TestResolveFlowIPsViaK8s_FetchErrorKeepsGraphLookups(t *testing.T) {
	liveNode := mkK8sNode("node-live", "ip-10-1-0-5", "10.1.0.5")
	existing := []*core.DbNode{liveNode}
	fetch := func(string, []string, time.Time, time.Time) ([]map[string]string, []map[string]string, error) {
		return nil, nil, errors.New("agent not connected")
	}

	end := time.Now()
	got, errs := resolveFlowIPsViaK8s([]string{"10.1.0.5", "10.1.1.1"}, []string{testK8sAccount},
		existing, NewK8sNodeIPResolver(existing), fetch, end.Add(-time.Hour), end)

	if len(errs) != 1 || !isAgentNotConnected(errs[0]) {
		t.Errorf("errs = %v, want one agent-not-connected error", errs)
	}
	if r := got["10.1.0.5"]; r.node == nil || r.node.ID != "node-live" {
		t.Errorf("10.1.0.5 = %+v, want the live K8s node despite the metrics error", r)
	}
	if _, ok := got["10.1.1.1"]; ok {
		t.Errorf("10.1.1.1 should stay unresolved without metrics, got %+v", got["10.1.1.1"])
	}
}

func TestResolveFlowIPsViaK8s_NoMappedK8sAccountSkipsFetch(t *testing.T) {
	called := false
	fetch := func(string, []string, time.Time, time.Time) ([]map[string]string, []map[string]string, error) {
		called = true
		return nil, nil, nil
	}
	end := time.Now()
	got, _ := resolveFlowIPsViaK8s([]string{"10.1.1.1"}, nil, nil, nil, fetch, end.Add(-time.Hour), end)
	if called || len(got) != 0 {
		t.Errorf("no mapped K8s account: fetch called=%v, results=%v; want no call, no results", called, got)
	}
}

// Pod IPs resolved through K8s land on the existing Workload, and several of
// one workload's pod IPs aggregate into a single edge. Addresses nothing
// resolves collapse into one node per VPC, with the IPs on the edge.
func TestBuildEdgesFromConns_K8sResolvedAndGrouped(t *testing.T) {
	s := NewVPCFlowLogsFlowSource(slog.Default())
	caller := mkNode("i-checkout", core.NodeTypeComputeInstance, "checkout")
	workload := mkWorkload("wl-services", "nudgebee", "Deployment", "services-server")
	ipIndex := map[string]*core.DbNode{"10.0.1.20": caller}
	k8sResolved := map[string]flowIPResolution{
		"10.0.3.1": {node: workload, tag: resolutionK8sPodIP},
		"10.0.3.2": {node: workload, tag: resolutionK8sPodIP},
		"10.0.9.1": {reason: reasonVPCIPAmbiguousOwner},
	}
	conns := []flowConn{
		{SrcIP: "10.0.1.20", DstIP: "10.0.3.1", DstPort: 8000, Protocol: 6, Action: "ACCEPT", Bytes: 100, Connections: 1, Scope: "vpc-1"},
		{SrcIP: "10.0.1.20", DstIP: "10.0.3.2", DstPort: 8000, Protocol: 6, Action: "ACCEPT", Bytes: 50, Connections: 1, Scope: "vpc-1"},
		{SrcIP: "10.0.1.20", DstIP: "10.0.9.1", DstPort: 5432, Protocol: 6, Action: "ACCEPT", Bytes: 7, Connections: 1, Scope: "vpc-1"},
		{SrcIP: "10.0.1.20", DstIP: "10.0.9.2", DstPort: 6379, Protocol: 6, Action: "ACCEPT", Bytes: 3, Connections: 1, Scope: "vpc-1"},
	}

	end := time.Now()
	edges, nodes, unresolved, err := s.buildEdgesFromConns(conns, ipIndex, k8sResolved, "tenant-1", "acct-1", end.Add(-time.Hour), end)
	if err != nil {
		t.Fatalf("buildEdgesFromConns error: %v", err)
	}

	if len(nodes) != 1 {
		t.Fatalf("created %d nodes, want 1 grouped node for vpc-1: %+v", len(nodes), nodes)
	}
	group := nodes[0]
	if group.Properties["name"] != "Unidentified addresses (vpc-1)" || group.Properties["vpc_id"] != "vpc-1" ||
		group.Properties["subtype"] != "vpc-unresolved-ip" {
		t.Errorf("grouped node properties = %v", group.Properties)
	}

	var toWorkload, toGroup *core.DbEdge
	for _, e := range edges {
		switch e.DestinationNodeID {
		case "wl-services":
			toWorkload = e
		case group.ID:
			toGroup = e
		}
	}
	if len(edges) != 2 || toWorkload == nil || toGroup == nil {
		t.Fatalf("got %d edges (workload=%v group=%v), want exactly one to each", len(edges), toWorkload != nil, toGroup != nil)
	}
	if got := toWorkload.Properties["total_bytes"].(int64); got != 150 {
		t.Errorf("workload edge total_bytes = %d, want 150 (two pod IPs aggregated)", got)
	}
	if toWorkload.Properties["resolution_source_dst"] != resolutionK8sPodIP {
		t.Errorf("workload edge resolution_source_dst = %v, want %s", toWorkload.Properties["resolution_source_dst"], resolutionK8sPodIP)
	}
	if ips, _ := toGroup.Properties["unresolved_dst_ips"].([]string); len(ips) != 2 || ips[0] != "10.0.9.1" || ips[1] != "10.0.9.2" {
		t.Errorf("group edge unresolved_dst_ips = %v, want [10.0.9.1 10.0.9.2]", toGroup.Properties["unresolved_dst_ips"])
	}

	if len(unresolved) != 2 {
		t.Fatalf("unresolved = %+v, want the two ungrouped IPs", unresolved)
	}
	for _, u := range unresolved {
		if u.Scope != "vpc-1" || !u.Peers["checkout"] {
			t.Errorf("unresolved %s = %+v, want scope vpc-1 with peer checkout", u.IP, u)
		}
	}
}

func TestVPCForFlowLog(t *testing.T) {
	subnet := mkNode("sn", core.NodeTypeSubnet, "subnet-1")
	subnet.CloudAccountID = "acct-1"
	subnet.Properties["subnet_id"] = "subnet-1"
	subnet.Properties["vpc_id"] = "vpc-from-subnet"
	eni := mkNode("eni", core.NodeTypeNetworkInterface, "eni-1")
	eni.CloudAccountID = "acct-1"
	eni.Properties["resource_id"] = "eni-1"
	eni.Properties["vpc_id"] = "vpc-from-eni"
	otherAcct := mkNode("sn2", core.NodeTypeSubnet, "subnet-2")
	otherAcct.CloudAccountID = "acct-2"
	otherAcct.Properties["subnet_id"] = "subnet-2"
	otherAcct.Properties["vpc_id"] = "vpc-other"
	nodes := []*core.DbNode{subnet, eni, otherAcct}

	cases := map[string]string{
		"vpc-direct": "vpc-direct",
		"subnet-1":   "vpc-from-subnet",
		"eni-1":      "vpc-from-eni",
		"subnet-2":   "", // belongs to another account
		"":           "",
	}
	for resource, want := range cases {
		if got := vpcForFlowLog(resource, nodes, "acct-1"); got != want {
			t.Errorf("vpcForFlowLog(%q) = %q, want %q", resource, got, want)
		}
	}
}

func TestIPAlternation(t *testing.T) {
	if got, want := ipAlternation([]string{"10.0.0.1", "172.31.4.9"}), `10\\.0\\.0\\.1|172\\.31\\.4\\.9`; got != want {
		t.Errorf("ipAlternation = %s, want %s", got, want)
	}
}
