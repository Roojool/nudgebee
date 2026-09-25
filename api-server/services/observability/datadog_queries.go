package observability

import (
	"fmt"
	"strings"
)

// Datadog utilisation query builders. These render abstract metric keys into
// Datadog metric query strings for node- and workload-scoped utilisation, and
// are dispatched from FetchMetricUtilisation (see service.go). Kept in their own
// file mirroring the per-provider layout (solarwinds_queries.go, dynatrace_queries.go).

// --- DATADOG HELPERS ---

func buildDatadogNodeQueries(meta RequestMetadata, metrics []string) map[string]string {
	queries := make(map[string]string)
	if meta.NodeName == "" {
		return queries // Or return error if strict validation is needed
	}

	// kube_node, not host. Datadog's `host` tag is the cloud instance's fully
	// qualified name — on GKE `gke-…-fiik.us-central1-a.c.<project>.internal` — while
	// NodeName is the Kubernetes node name Prometheus and the API server use. An
	// exact `host:` match on the short name therefore matches nothing, silently: the
	// whole node scope returned zero series on a node with 69 running containers.
	// `kube_node` carries the Kubernetes name and is present on every metric this
	// function reads, including the Datadog agent's own system.* host metrics.
	filterStr := fmt.Sprintf("kube_node:%s", meta.NodeName)
	groupBy := " by {kube_node}"

	// Per-pod keys narrow to the node through the same filter, but group by the
	// container rather than the host — the node is the scope, not the answer.
	windowSeconds := int64(datadogRollupSeconds(meta.RangeWindow))

	for _, metricKey := range metrics {
		if metric, perPod := datadogPerPodKeys[metricKey]; perPod {
			queries[metricKey] = buildDatadogPerPodQuery(metric, meta, filterStr, windowSeconds)
			continue
		}

		switch metricKey {
		case "cpu_total":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes_state.node.cpu_allocatable{%s}%s", filterStr, groupBy)
		case "mem_total":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes_state.node.memory_allocatable{%s}%s", filterStr, groupBy)
		case "cpu_usage":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.usage.total{%s}%s", filterStr, groupBy)
		case "memory_usage":
			queries[metricKey] = fmt.Sprintf("avg:system.mem.used{%s}%s", filterStr, groupBy)
		case "cpu_request":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.requests{%s}%s", filterStr, groupBy)
		case "cpu_limit":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.limits{%s}%s", filterStr, groupBy)
		case "memory_request":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.memory.requests{%s}%s", filterStr, groupBy)
		case "memory_limit":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.memory.limits{%s}%s", filterStr, groupBy)
		case "disk_total":
			queries[metricKey] = fmt.Sprintf("avg:system.disk.total{%s}%s", filterStr, groupBy)
		case "disk_used":
			queries[metricKey] = fmt.Sprintf("avg:system.disk.used{%s}%s", filterStr, groupBy)
		case "cpu_usage_line":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.usage.total{%s}%s", filterStr, groupBy)
		case "memory_usage_line":
			queries[metricKey] = fmt.Sprintf("avg:system.mem.used{%s}%s", filterStr, groupBy)
		case "pvc_usage":
			queries[metricKey] = fmt.Sprintf("(avg:system.disk.used{%s}%s / avg:system.disk.total{%s}%s) * 100", filterStr, groupBy, filterStr, groupBy)
		}
	}
	return queries
}

// datadogPVCPerClaimQueries renders the per-claim volume keys, one series per claim.
//
// Separate from the switch in buildDatadogWorkloadQueries because those keys are the
// only ones that answer with no workload scope, and that function returns before its
// switch when there is none. A claim belongs to a namespace, not to a workload; volume
// rightsizing asks about every claim in the cluster and matches them to workloads
// afterwards, from Kubernetes rather than from metrics.
func buildDatadogPVCPerClaimQueries(queries map[string]string, meta RequestMetadata, metrics []string) {
	scope := "*"
	switch {
	case meta.Namespace != "" && meta.PVCName != "":
		scope = fmt.Sprintf("kube_namespace:%s, persistentvolumeclaim:%s", meta.Namespace, meta.PVCName)
	case meta.Namespace != "":
		scope = fmt.Sprintf("kube_namespace:%s", meta.Namespace)
	}

	for _, metricKey := range metrics {
		var metric string
		switch metricKey {
		case "pvc_used_by_claim":
			metric = "used_bytes"
		case "pvc_capacity_by_claim":
			metric = "capacity_bytes"
		default:
			continue
		}
		queries[metricKey] = fmt.Sprintf(
			"max:kubernetes.kubelet.volume.stats.%s{%s} by {persistentvolumeclaim,kube_namespace}", metric, scope)
	}
}

// datadogClusterWideKeys are scoped {*}: they describe the whole cluster and mean the
// same thing with or without a workload filter.
//
// They used to sit below buildDatadogWorkloadQueries' `if filterStr == ""` guard, so
// the one scope they exist for — the cluster utilisation tiles, which name no
// namespace and no workload — returned before reaching them. Measured live against a
// Datadog account: every cluster tile came back empty, and empty with NO note, which
// reads as "this cluster used nothing" rather than "nobody built a query".
var datadogClusterWideKeys = map[string]string{
	"cpu_real":        "sum:kubernetes.cpu.usage.total{*}",
	"cpu_total":       "sum:kubernetes_state.node.cpu_capacity{*}",
	"cpu_usage_trend": "sum:kubernetes.cpu.usage.total{*}",
	"mem_real":        "sum:kubernetes.memory.usage{*}",
	"mem_total":       "sum:kubernetes_state.node.memory_capacity{*}",
	"mem_usage_trend": "sum:kubernetes.memory.usage{*}",
}

func buildDatadogClusterWideQueries(queries map[string]string, metrics []string) {
	for _, metricKey := range metrics {
		if query, ok := datadogClusterWideKeys[metricKey]; ok {
			queries[metricKey] = query
		}
	}
}

func buildDatadogWorkloadQueries(meta RequestMetadata, metrics []string) map[string]string {
	// Per-pod keys roll up over the window, so they need its width in seconds.
	// datadogRollupSeconds parses the "<N>s" form promAggWindow (or an explicit
	// Step) produces.
	windowSeconds := int64(datadogRollupSeconds(meta.RangeWindow))

	queries := make(map[string]string)

	var tagKey string
	switch meta.Kind {
	case "deployment":
		tagKey = "kube_deployment"
	case "statefulset":
		tagKey = "kube_stateful_set"
	case "daemonset":
		tagKey = "kube_daemon_set"
	case "pod":
		tagKey = "pod_name"
	default:
		tagKey = "kube_deployment"
	}

	var filterStr string
	if meta.Name != "" && meta.Namespace != "" {
		filterStr = fmt.Sprintf("kube_namespace:%s, %s:%s", meta.Namespace, tagKey, meta.Name)
	} else if meta.Namespace != "" {
		filterStr = fmt.Sprintf("kube_namespace:%s", meta.Namespace)
	}

	// --- NEW: Append Container Filter if present ---
	if filterStr != "" && meta.ContainerName != "" {
		filterStr = fmt.Sprintf("%s, kube_container_name:%s", filterStr, meta.ContainerName)
	}

	// Both families are answered before the guard below, because both mean something
	// without a workload: volume rightsizing walks every claim in the cluster, and the
	// cluster tiles name no namespace at all. Everything after the guard needs a
	// workload filter.
	buildDatadogPVCPerClaimQueries(queries, meta, metrics)
	buildDatadogClusterWideQueries(queries, metrics)

	if filterStr == "" {
		return queries
	}

	groupBy := fmt.Sprintf(" by {%s}", tagKey)

	// PVC filter for Datadog
	var pvcFilterStr string
	if meta.Namespace != "" {
		if meta.PVCName != "" {
			pvcFilterStr = fmt.Sprintf("kube_namespace:%s, persistentvolumeclaim:%s", meta.Namespace, meta.PVCName)
		} else if meta.Name != "" {
			pvcFilterStr = fmt.Sprintf("kube_namespace:%s, persistentvolumeclaim:%s-*", meta.Namespace, meta.Name)
		} else {
			pvcFilterStr = fmt.Sprintf("kube_namespace:%s", meta.Namespace)
		}
	}

	for _, metricKey := range metrics {
		// Per-pod keys group by pod and container instead of the workload tag, and
		// carry their own rollup. One Datadog has no metric for builds no query;
		// datadogNoteUnanswerableKeys puts it back in the response with the
		// reason, so it is not mistaken for a workload that used nothing.
		if metric, perPod := datadogPerPodKeys[metricKey]; perPod {
			queries[metricKey] = buildDatadogPerPodQuery(metric, meta, filterStr, windowSeconds)
			continue
		}

		switch metricKey {
		// (Cases remain identical, they just use the updated filterStr)
		case "network_receive_packet":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.network.rx_bytes{%s}%s", filterStr, groupBy)
		case "network_transmit_packets":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.network.tx_bytes{%s}%s * -1", filterStr, groupBy)
		// HTTP metrics come from APM trace metrics, not the kube_* series above — see
		// datadog_http_queries.go for why they need their own filter and operation name.
		case "http_throughput", "http_latency_p95", "http_latency_p99", "http_latency_sum",
			"http_error_rate", "http_status", "http_max_response_time":
			if q := buildDatadogHTTPQuery(metricKey, meta); q != "" {
				queries[metricKey] = q
			}
		case "network_usage":
			queries[metricKey] = fmt.Sprintf("default(avg:container.net.tcp.connection.time.seconds.total{%s}%s, avg:kubernetes.network.rx_bytes{%s}%s)", filterStr, groupBy, filterStr, groupBy)
		case "cpu_usage":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.usage.total{%s}%s", filterStr, groupBy)
		// Seconds per second the workload's containers spent throttled, from the
		// Datadog agent's kubelet check. It carries the same kube_* tags as usage, so
		// filterStr scopes it to the workload exactly.
		case "cpu_throttling":
			queries[metricKey] = fmt.Sprintf("sum:kubernetes.cpu.cfs.throttled.seconds{%s}%s", filterStr, groupBy)
		// APM latency. Trace metrics carry no kube_* tags, only service/env/resource,
		// so this matches on the APM service being named after the workload. Where it
		// is not, the query matches nothing and the card is empty.
		case "workload_http_latency_p99":
			if meta.Name != "" {
				queries[metricKey] = fmt.Sprintf("p99:trace.http.server.request{service:%s}", meta.Name)
			}
		// The same two concepts, from APM rather than from eBPF counters. Datadog's
		// trace metrics already exclude what the probe list excludes on Prometheus:
		// kubelet probes and /metrics scrapes are not traced, because nothing
		// instruments them. So the definition holds without a path filter, which is
		// the point of naming the metric rather than the query.
		case "workload_http_request_rate":
			if meta.Name != "" {
				queries[metricKey] = fmt.Sprintf("sum:trace.http.server.request.hits{service:%s}.as_rate()", meta.Name)
			}
		case "workload_http_latency_mean":
			if meta.Name != "" {
				queries[metricKey] = fmt.Sprintf("avg:trace.http.server.request{service:%s}", meta.Name)
			}
		case "cpu_request":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.requests{%s}%s", filterStr, groupBy)
		case "cpu_limit":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.cpu.limits{%s}%s", filterStr, groupBy)
		case "memory_usage":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.memory.working_set{%s}%s", filterStr, groupBy)
		case "memory_request":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.memory.requests{%s}%s", filterStr, groupBy)
		case "memory_limit":
			queries[metricKey] = fmt.Sprintf("avg:kubernetes.memory.limits{%s}%s", filterStr, groupBy)

		// --- PVC Metrics ---
		case "pvc_usage":
			if pvcFilterStr != "" {
				queries[metricKey] = fmt.Sprintf("sum:kubernetes.kubelet.volume.stats.used_bytes{%s}", pvcFilterStr)
			}
		case "pvc_requests":
			if pvcFilterStr != "" {
				queries[metricKey] = fmt.Sprintf("sum:kubernetes_state.persistentvolumeclaim.request_storage{%s}", pvcFilterStr)
			}

		case "p90_mem":
			queries[metricKey] = "avg:system.mem.used{*} by {host}"
		case "p90_cpu":
			queries[metricKey] = "avg:kubernetes.cpu.usage.total{*} by {host}"
		case "p50_mem":
			queries[metricKey] = "avg:system.mem.used{*} by {host}"
		case "p50_cpu":
			queries[metricKey] = "avg:kubernetes.cpu.usage.total{*} by {host}"
		case "max_usage_mem":
			queries[metricKey] = "max:system.mem.used{*}"
		case "max_usage_cpu":
			queries[metricKey] = "max:kubernetes.cpu.usage.total{*}"
		case "replica_defined":
			queries[metricKey] = datadogWorkloadReplicaQuery(meta, false)
		case "replica_ready":
			queries[metricKey] = datadogWorkloadReplicaQuery(meta, true)
		}
	}
	return queries
}

// datadogWorkloadReplicaQuery renders replica_defined / replica_ready for the workload
// named in meta.
//
// The keys used to read the ReplicaSet family through the glob
// `kube_replica_set:<name>-*`, which matches every ReplicaSet of every workload sharing
// the prefix ("services-server-*" also catches "services-server-worker-..."), and every
// revision of the right one. Reading the controller's own object gives one series per
// workload. Mirrors promWorkloadReplicaQuery; the kind-to-tag mapping is the one
// buildDatadogWorkloadQueries already uses for the usage keys.
func datadogWorkloadReplicaQuery(meta RequestMetadata, ready bool) string {
	var metric, tag string
	switch strings.ToLower(meta.Kind) {
	case "statefulset":
		tag, metric = "kube_stateful_set", "kubernetes_state.statefulset.replicas_desired"
		if ready {
			metric = "kubernetes_state.statefulset.replicas_ready"
		}
	case "daemonset":
		tag, metric = "kube_daemon_set", "kubernetes_state.daemonset.desired"
		if ready {
			metric = "kubernetes_state.daemonset.ready"
		}
	case "replicaset":
		tag, metric = "kube_replica_set", "kubernetes_state.replicaset.replicas_desired"
		if ready {
			metric = "kubernetes_state.replicaset.replicas_ready"
		}
	default:
		tag, metric = "kube_deployment", "kubernetes_state.deployment.replicas_desired"
		if ready {
			metric = "kubernetes_state.deployment.replicas_ready"
		}
	}
	// max, not sum: see promWorkloadReplicaQuery. One workload is one number, however
	// many Cluster Agents report it.
	return fmt.Sprintf("max:%s{kube_namespace:%s, %s:%s}", metric, meta.Namespace, tag, meta.Name)
}
