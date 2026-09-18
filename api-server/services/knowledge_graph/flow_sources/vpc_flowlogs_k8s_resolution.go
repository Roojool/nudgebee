package flow_sources

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	integrationscore "nudgebee/services/integrations/core"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/observability"
	"nudgebee/services/security"
)

// Kubernetes resolution for VPC flow-log IPs.
//
// Under the AWS VPC CNI every pod gets a real VPC address, so most private IPs
// in a flow log are pods — and EKS rotates nodes (and with them ENIs and pod
// IPs) often enough that the ENI index rarely still knows them by build time.
// kube_pod_info / kube_node_info carry the IP → owner mapping with history, so
// this resolves each IP the ENI index missed against the pods and nodes that
// held it *during the flow window*, not the ones alive now: an IP is reused by
// a different pod within hours, and "alive now" misses almost every pod on a
// churning cluster.

const (
	// Resolution tags, stamped into resolution_source_src/dst on the edge.
	resolutionK8sPodIP  = "k8s-pod-ip"
	resolutionK8sNodeIP = "k8s-node-ip"

	// Review-table reason codes (core.RecordUncertainClassification). One per
	// distinct reason the IP stayed unresolved; see knowledge_graph/CLAUDE.md.
	reasonVPCIPUnresolved      = "vpc_ip_unresolved"
	reasonVPCIPK8sNodeGone     = "vpc_ip_k8s_node_gone"
	reasonVPCIPAmbiguousOwner  = "vpc_ip_ambiguous_owner"
	reasonVPCIPWorkloadNotInKG = "vpc_ip_workload_not_in_graph"

	// IPs per regex alternation, keeping each PromQL selector a sane length.
	vpcFlowK8sQueryChunkSize      = 200
	vpcFlowPodInfoQueryKeyPrefix  = "pods_"
	vpcFlowNodeInfoQueryKeyPrefix = "nodes_"
)

// flowIPResolution is the outcome of resolving one flow-log IP beyond the ENI
// index: either a node (with its tag) or the reason it stayed unresolved plus
// the evidence behind that reason, for the review table.
type flowIPResolution struct {
	node   *core.DbNode
	tag    string
	reason string
	detail map[string]any
}

// k8sIPInfoFetcher returns kube_pod_info and kube_node_info label sets for the
// given IPs over [start, end]. Injected so resolution is testable without a
// metrics backend.
type k8sIPInfoFetcher func(k8sAccountID string, ips []string, start, end time.Time) (pods, nodes []map[string]string, err error)

// resolveFlowIPsViaK8s resolves ips against every K8s account mapped to the AWS
// account. Accounts are tried in sorted order so the outcome doesn't depend on
// map iteration: the first account that resolves an IP wins, and an unresolved
// IP keeps the first reason any account gave for it.
func resolveFlowIPsViaK8s(
	ips []string,
	k8sAccounts []string,
	existingNodes []*core.DbNode,
	nodeResolver *K8sNodeIPResolver,
	fetch k8sIPInfoFetcher,
	start, end time.Time,
) (map[string]flowIPResolution, []error) {
	out := make(map[string]flowIPResolution, len(ips))
	if len(ips) == 0 || len(k8sAccounts) == 0 {
		return out, nil
	}
	accounts := append([]string(nil), k8sAccounts...)
	sort.Strings(accounts)
	var errs []error
	for _, k8sAccount := range accounts {
		pending := make([]string, 0, len(ips))
		for _, ip := range ips {
			if r, ok := out[ip]; !ok || r.node == nil {
				pending = append(pending, ip)
			}
		}
		if len(pending) == 0 {
			break
		}
		pods, nodes, err := fetch(k8sAccount, pending, start, end)
		if err != nil {
			// Non-fatal: node IPs still resolve from the graph below.
			errs = append(errs, fmt.Errorf("k8s account %s: %w", k8sAccount, err))
		}
		workloads := indexWorkloadsByOwner(existingNodes, k8sAccount)
		for ip, r := range resolveK8sIPsForAccount(pending, k8sAccount, nodeResolver, workloads, pods, nodes) {
			if prev, ok := out[ip]; ok && (prev.node != nil || r.node == nil) {
				continue
			}
			out[ip] = r
		}
	}
	return out, errs
}

// resolveK8sIPsForAccount is the pure resolution step for one K8s account.
// Order per IP:
//  1. an active K8s Node already in the graph owns it as internal_ip;
//  2. the IP was a node IP during the window but that node is gone — never
//     attributed to a pod, because host-network pods (aws-node, kube-proxy,
//     node-exporter) share it and would be a wrong guess;
//  3. exactly one pod owner (Workload) held it during the window.
//
// More than one owner in the window is ambiguous and left unresolved rather
// than guessed.
func resolveK8sIPsForAccount(
	ips []string,
	k8sAccount string,
	nodeResolver *K8sNodeIPResolver,
	workloads workloadIndex,
	podRows, nodeRows []map[string]string,
) map[string]flowIPResolution {
	podsByIP := make(map[string][]map[string]string)
	for _, r := range podRows {
		if ip := r["pod_ip"]; ip != "" {
			podsByIP[ip] = append(podsByIP[ip], r)
		}
	}
	nodeNameByIP := make(map[string]string)
	for _, r := range nodeRows {
		if ip := r["internal_ip"]; ip != "" {
			nodeNameByIP[ip] = r["node"]
		}
	}

	out := make(map[string]flowIPResolution)
	for _, ip := range ips {
		if n, ok := nodeResolver.Resolve(k8sAccount, "", ip); ok {
			out[ip] = flowIPResolution{node: n, tag: resolutionK8sNodeIP}
			continue
		}
		if nodeName, ok := nodeNameByIP[ip]; ok {
			out[ip] = flowIPResolution{
				reason: reasonVPCIPK8sNodeGone,
				detail: map[string]any{"k8s_account_id": k8sAccount, "k8s_node": nodeName},
			}
			continue
		}

		owners := make(map[string]*core.DbNode)
		ownerNames := make(map[string]bool)
		for _, r := range podsByIP[ip] {
			if r["host_network"] == "true" {
				continue // shares its node's IP; handled by the node rules above
			}
			namespace, podName := r["namespace"], r["pod"]
			kind, name := resolveOwner(r["created_by_kind"], r["created_by_name"])
			if name == "" {
				kind, name = "Pod", podName
			}
			if namespace == "" || name == "" {
				continue
			}
			// Cluster is part of the owner's identity: the same namespace/kind/name
			// in two clusters is two owners, not one.
			owner := namespace + "/" + kind + "/" + name
			if cluster := r["k8s_cluster"]; cluster != "" {
				owner = cluster + "/" + owner
			}
			ownerNames[owner] = true
			if n, ok := workloads.lookup(r["k8s_cluster"], namespace, kind, name, podName); ok {
				owners[n.ID] = n
			}
		}

		switch {
		case len(ownerNames) == 0:
			continue
		case len(ownerNames) > 1 || len(owners) > 1:
			out[ip] = flowIPResolution{
				reason: reasonVPCIPAmbiguousOwner,
				detail: map[string]any{"k8s_account_id": k8sAccount, "k8s_owners": sortedKeys(ownerNames)},
			}
		case len(owners) == 1:
			for _, n := range owners {
				out[ip] = flowIPResolution{node: n, tag: resolutionK8sPodIP}
			}
		default:
			out[ip] = flowIPResolution{
				reason: reasonVPCIPWorkloadNotInKG,
				detail: map[string]any{"k8s_account_id": k8sAccount, "k8s_owners": sortedKeys(ownerNames)},
			}
		}
	}
	return out
}

// fetchK8sIPInfo is the production k8sIPInfoFetcher: one metrics call per chunk
// of IPs, each carrying a kube_pod_info and a kube_node_info query evaluated
// at the end of the flow window over the window's length.
func fetchK8sIPInfo(reqCtx *security.RequestContext) k8sIPInfoFetcher {
	return func(k8sAccountID string, ips []string, start, end time.Time) ([]map[string]string, []map[string]string, error) {
		valid := make([]string, 0, len(ips))
		for _, ip := range ips {
			// Validated before interpolation into PromQL.
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
				valid = append(valid, ip)
			}
		}
		window := int(end.Sub(start).Seconds())
		if window <= 0 || len(valid) == 0 {
			return nil, nil, nil
		}

		queries := make(map[string]string)
		for i := 0; i < len(valid); i += vpcFlowK8sQueryChunkSize {
			chunk := valid[i:min(i+vpcFlowK8sQueryChunkSize, len(valid))]
			re := ipAlternation(chunk)
			queries[fmt.Sprintf("%s%d", vpcFlowPodInfoQueryKeyPrefix, i)] = fmt.Sprintf(
				`max by (pod_ip, namespace, pod, created_by_kind, created_by_name, host_network, k8s_cluster) (max_over_time(kube_pod_info{pod_ip=~"%s"}[%ds]))`,
				re, window)
			queries[fmt.Sprintf("%s%d", vpcFlowNodeInfoQueryKeyPrefix, i)] = fmt.Sprintf(
				`max by (internal_ip, node) (max_over_time(kube_node_info{internal_ip=~"%s"}[%ds]))`,
				re, window)
		}

		resp, err := observability.FetchMetricsQuery(reqCtx, observability.FetchMetricsRequest{
			AccountId:      k8sAccountID,
			MetricProvider: "prometheus",
			Queries:        queries,
			StartTime:      start.UnixMilli(),
			EndTime:        end.UnixMilli(),
			Instant:        true,
		})
		if err != nil {
			return nil, nil, err
		}
		var pods, nodes []map[string]string
		for key := range queries {
			rows := observability.PromQLLabels(resp, key)
			if strings.HasPrefix(key, vpcFlowPodInfoQueryKeyPrefix) {
				pods = append(pods, rows...)
			} else {
				nodes = append(nodes, rows...)
			}
		}
		return pods, nodes, nil
	}
}

// ipAlternation builds a PromQL regex alternation of literal IPs. The dots are
// escaped for the regex, and the backslash doubled for the PromQL string.
func ipAlternation(ips []string) string {
	escaped := make([]string, len(ips))
	for i, ip := range ips {
		escaped[i] = strings.ReplaceAll(ip, ".", `\\.`)
	}
	return strings.Join(escaped, "|")
}

// isAgentNotConnected reports whether a metrics error only means the cluster's
// agent is offline — expected for many accounts, so not worth a warning.
func isAgentNotConnected(err error) bool {
	return err != nil && integrationscore.IsAgentNotConnectedError(err)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
