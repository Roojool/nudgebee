package observability

import "fmt"

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

	filterStr := fmt.Sprintf("host:%s", meta.NodeName)
	groupBy := " by {host}"

	for _, metricKey := range metrics {
		switch metricKey {
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

func buildDatadogWorkloadQueries(meta RequestMetadata, metrics []string) map[string]string {
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

		// --- Node/Cluster Aggregations ---
		case "cpu_real":
			queries[metricKey] = "sum:kubernetes.cpu.usage.total{*}"
		case "cpu_total":
			queries[metricKey] = "sum:kubernetes_state.node.cpu_capacity{*}"
		case "mem_real":
			queries[metricKey] = "sum:kubernetes.memory.usage{*}"
		case "mem_total":
			queries[metricKey] = "sum:kubernetes_state.node.memory_capacity{*}"
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
			queries[metricKey] = fmt.Sprintf("sum:kubernetes_state.replicaset.replicas_desired{kube_namespace:%s, kube_replica_set:%s-*}", meta.Namespace, meta.Name)
		case "replica_ready":
			queries[metricKey] = fmt.Sprintf("sum:kubernetes_state.replicaset.replicas_ready{kube_namespace:%s, kube_replica_set:%s-*}", meta.Namespace, meta.Name)
		}
	}
	return queries
}
