package observability

import (
	"errors"
	"fmt"
	"nudgebee/services/eventrule/playbooks"
	"nudgebee/services/security"
	"sort"
	"time"
)

// noisy_neighbours_enricher composes Prometheus queries against the host
// node to identify the top memory-consuming co-tenant pods.
//
// Output shape:
//
//	{
//	  "name": "noisy_neighbours",
//	  "data": {
//	    "node_name":          "<node>",
//	    "memory_used":        <bytes>,
//	    "memory_allocatable": <bytes>,
//	    "total_pods":         N,
//	    "neighbours":         [{pod_name, namespace, memory_used}, ...]
//	  }
//	}
func init() {
	playbooks.RegisterAction("noisy_neighbours_enricher", &noisyNeighboursAction{})
}

type noisyNeighboursAction struct{}

// Events worth asking "is something else on this node to blame?" about.
//
// The first group comes from the agent's own matchers and already carries the
// node. The second group is Prometheus / kube-state alerts about a single pod:
// they are the signals that actually point at contention — a throttled
// container, a pod that never became ready, a container stuck waiting — and
// they were all excluded, so the one question this enricher exists to answer
// was never asked for them. They carry no node (measured: 3589/3589 such
// events in a week on prod had neither a node nor an instance label), which is
// why nodeForSubjectPod below resolves it from the pod inventory.
var noisyNeighboursAggKeys = map[string]bool{
	"pod_oom_killer_enricher": true,
	"report_crash_loop":       true,
	"KubePodCrashLooping":     true,
	"CPUThrottlingHigh":       true,
	"KubePodNotReady":         true,
	"KubeContainerWaiting":    true,
	// Kubelet warnings arrive under one key; a failing readiness probe —
	// the textbook symptom of a neighbour eating the node — is one of them.
	"Kubernetes Warning Event": true,
}

// We query Prometheus over a short RANGE window ending at the incident and
// take the latest sample of each series, rather than a single instant query.
// The agent's live instant batch returns an empty result set at
// event-processing time — for the same OOM event the range-based
// pod_metric / pod_node_metrics cards populate while the instant
// noisy-neighbours card comes back all-zero — so we use the proven range
// path. topk(15) evaluated over a range can surface more than 15 distinct
// series across steps, so we cap to the top N after sorting by latest value.
const (
	noisyNeighboursTopN            = 15
	noisyNeighboursLookbackMinutes = 10
)

// Indirected so a test can exercise the card's ranking and payload without a live
// metrics backend. There is no provider dispatch left to drive: the layer resolves
// the account's backend, so what remains worth testing is what the card does with
// what comes back.
var fetchNoisyNeighbourMetrics = fetchNoisyNeighbours

func (a *noisyNeighboursAction) CanAutoExecute(ctx playbooks.PlaybookActionContext) bool {
	if !noisyNeighboursAggKeys[ctx.GetEvent().AggregationKey] {
		return false
	}
	name, ns := playbooks.SubjectPodNamespace(ctx.GetEvent())
	if name == "" || ns == "" {
		return false
	}
	// Need the host node to filter peers. Agent-sourced events carry it on
	// the event; alert-sourced ones do not, and are resolved from inventory.
	return noisyNeighboursNodeName(ctx) != ""
}

func (a *noisyNeighboursAction) AutoExecute(ctx playbooks.PlaybookActionContext) (playbooks.PlaybookActionResponse, error) {
	podName, namespace := playbooks.SubjectPodNamespace(ctx.GetEvent())
	return a.Execute(ctx, map[string]any{
		"pod_name":  podName,
		"namespace": namespace,
		"node_name": noisyNeighboursNodeName(ctx),
	})
}

func (a *noisyNeighboursAction) Execute(ctx playbooks.PlaybookActionContext, rawParams map[string]any) (playbooks.PlaybookActionResponse, error) {
	podName, _ := rawParams["pod_name"].(string)
	namespace, _ := rawParams["namespace"].(string)
	nodeName, _ := rawParams["node_name"].(string)
	if nodeName == "" {
		nodeName = noisyNeighboursNodeName(ctx)
	}
	if podName == "" || namespace == "" {
		return nil, errors.New("noisy_neighbours_enricher: pod_name + namespace required")
	}
	if nodeName == "" {
		return nil, errors.New("noisy_neighbours_enricher: no node_name on event (subject_node empty)")
	}

	tenantID, accountID := ctx.GetTenantId(), ctx.GetAccountId()
	if tenantID == "" || accountID == "" {
		return nil, errors.New("noisy_neighbours_enricher: tenant ID and account ID required")
	}
	requestCtx := security.NewRequestContextForTenantAdmin(tenantID, ctx.GetLogger(), nil, nil)

	start, end := playbooks.RangeQueryWindow(ctx.GetEvent(), noisyNeighboursLookbackMinutes, time.Now().UTC())
	data, err := fetchNoisyNeighbourMetrics(requestCtx, accountID, nodeName, start, end)
	if err != nil {
		return nil, err
	}
	if data == nil {
		// No implementation can answer for this account. Leaving the event without
		// the card is honest; an empty card would claim the node is idle.
		return nil, nil
	}
	return noisyNeighboursResponse(podName, namespace, rawParams, data)
}

type noisyNeighbourData struct {
	NodeName        string
	NodeUsed        float64
	NodeAllocatable float64
	TotalRequested  float64
	Neighbours      []map[string]any

	// CPUMeasured is set only when at least one container reported CPU usage.
	// Left false, the renderer omits the CPU keys entirely rather than reporting
	// a hard zero — "we did not measure it" and "nothing is using CPU" must not
	// look the same to a reader.
	CPUMeasured     bool
	NodeCPUUsed     float64
	NodeCPUCapacity float64
	CPUNeighbours   []map[string]any
}

// noisyNeighbourKeys are the two families the card ranks, each fetched with the
// statistic that suits it. They go in separate requests because `aggregate` applies
// to a whole request, and memory and CPU want different reductions.
var (
	noisyNeighbourMemoryKeys = []string{
		"container_memory_working_set_by_pod",
		"container_memory_request_by_pod",
		"container_memory_limit_by_pod",
		"mem_total",
	}
	noisyNeighbourCPUKeys = []string{
		"container_cpu_usage_by_pod",
		"container_cpu_request_by_pod",
		"container_cpu_limit_by_pod",
		"cpu_total",
	}
)

// fetchNoisyNeighbours asks the utilisation layer which containers on a node are
// consuming what, and what each asked for.
//
// It names keys and a node, never a provider: api-server resolves the account's
// metrics backend and builds the native query. That replaces two hand-written
// implementations — one PromQL, one Elasticsearch — plus the dispatch that chose
// between them, which admitted only Prometheus and Chronosphere and silently skipped
// the card for Datadog, New Relic and Dynatrace.
//
// Two requests rather than one because `aggregate` is per request: memory is ranked on
// its PEAK over the window, CPU on its AVERAGE. That is what the Elasticsearch
// implementation already did, and it is the more useful reading for an evidence card
// on a node-pressure event — the peak is what caused the pressure. The PromQL
// implementation took the latest sample instead, so Prometheus numbers move slightly
// and the two providers now agree, which they did not before.
func fetchNoisyNeighbours(ctx *security.RequestContext, accountID, nodeName string, start, end time.Time) (*noisyNeighbourData, error) {
	ask := func(keys []string, aggregate string) (map[string][]latestSample, error) {
		output, err := FetchMetricUtilisation(ctx, GetUtilisationTrendRequest{
			AccountId: accountID,
			StartTime: start.UnixMilli(),
			EndTime:   end.UnixMilli(),
			Aggregate: aggregate,
			Request: map[string]any{
				"kind":      "node",
				"node_name": nodeName,
				"metrics":   keys,
			},
		})
		if err != nil {
			return nil, err
		}
		return latestValuesByKey(output), nil
	}

	memory, err := ask(noisyNeighbourMemoryKeys, "max")
	if err != nil {
		// A provider that cannot answer these keys is not an error the event should
		// carry: the card is skipped with the reason logged, which is what the
		// dispatch this replaces did for every provider it had no implementation for.
		ctx.GetLogger().Info("noisy_neighbours_enricher: metrics unavailable for this account; skipping the card",
			"account_id", accountID, "node", nodeName, "error", err)
		return nil, nil
	}
	cpu, err := ask(noisyNeighbourCPUKeys, "avg")
	if err != nil {
		ctx.GetLogger().Info("noisy_neighbours_enricher: cpu metrics unavailable; rendering the card without them",
			"account_id", accountID, "node", nodeName, "error", err)
		cpu = map[string][]latestSample{}
	}

	results := map[string][]latestSample{
		"top_pods":     memory["container_memory_working_set_by_pod"],
		"mem_requests": memory["container_memory_request_by_pod"],
		"mem_limits":   memory["container_memory_limit_by_pod"],

		"node_alloc":   memory["mem_total"],
		"top_pods_cpu": cpu["container_cpu_usage_by_pod"],
		"cpu_requests": cpu["container_cpu_request_by_pod"],
		"cpu_limits":   cpu["container_cpu_limit_by_pod"],

		"node_cpu_alloc": cpu["cpu_total"],
	}
	// CPU is reported only when something actually measured it, so "we did not
	// measure" and "nothing is using CPU" stay distinguishable in the card.
	cpuMeasured := len(results["top_pods_cpu"]) > 0 || len(results["node_cpu_alloc"]) > 0

	return buildNoisyNeighbourData(nodeName, results, cpuMeasured)
}

// buildNoisyNeighbourData ranks and trims what the layer returned. Provider-agnostic
// by construction: it reads canonical namespace / pod / container labels, which every
// builder now emits.
func buildNoisyNeighbourData(nodeName string, results map[string][]latestSample, cpuMeasured bool) (*noisyNeighbourData, error) {
	// Index requests / limits by (namespace, pod, container) for O(1)
	// lookup while iterating top_pods. kube-state-metrics emits one
	// series per (pod, container) per resource — no aggregation needed.
	memRequests := indexByPodContainer(results["mem_requests"])
	memLimits := indexByPodContainer(results["mem_limits"])
	totalRequested := 0.0
	for _, v := range memRequests {
		totalRequested += v
	}

	neighbours := []map[string]any{}
	if vec, ok := results["top_pods"]; ok {
		for _, s := range vec {
			pod := s.Metric["pod"]
			ns := s.Metric["namespace"]
			container := s.Metric["container"]
			key := ns + "/" + pod + "/" + container
			entry := map[string]any{
				"name":             container,
				"pod_name":         pod,
				"namespace":        ns,
				"node_name":        nodeName,
				"memory_used":      s.Value,
				"memory_requested": memRequests[key],
				"memory_limit":     memLimits[key],
			}
			neighbours = append(neighbours, entry)
		}
		sort.Slice(neighbours, func(i, j int) bool {
			vi, _ := neighbours[i]["memory_used"].(float64)
			vj, _ := neighbours[j]["memory_used"].(float64)
			return vi > vj
		})
		// topk(15) over a range can yield >15 distinct series across steps;
		// keep only the top N by latest value to match the instant semantics.
		if len(neighbours) > noisyNeighboursTopN {
			neighbours = neighbours[:noisyNeighboursTopN]
		}
	}

	// Same shape for CPU, ranked on its own. Kept as a separate list rather
	// than extra columns on `neighbours`: the pod hogging memory is usually not
	// the pod hogging CPU, and one list can only be sorted by one of them.
	cpuRequests := indexByPodContainer(results["cpu_requests"])
	cpuLimits := indexByPodContainer(results["cpu_limits"])
	cpuNeighbours := []map[string]any{}
	if vec, ok := results["top_pods_cpu"]; ok {
		for _, entry := range vec {
			pod := entry.Metric["pod"]
			ns := entry.Metric["namespace"]
			container := entry.Metric["container"]
			key := ns + "/" + pod + "/" + container
			cpuNeighbours = append(cpuNeighbours, map[string]any{
				"name":          container,
				"pod_name":      pod,
				"namespace":     ns,
				"node_name":     nodeName,
				"cpu_used":      entry.Value,
				"cpu_requested": cpuRequests[key],
				"cpu_limit":     cpuLimits[key],
			})
		}
		sort.Slice(cpuNeighbours, func(i, j int) bool {
			vi, _ := cpuNeighbours[i]["cpu_used"].(float64)
			vj, _ := cpuNeighbours[j]["cpu_used"].(float64)
			return vi > vj
		})
		if len(cpuNeighbours) > noisyNeighboursTopN {
			cpuNeighbours = cpuNeighbours[:noisyNeighboursTopN]
		}
	}

	// The node totals are the sum of the containers on it, not a node-level gauge.
	//
	// `memory_usage` and `cpu_usage` at node scope read node_memory_Active_bytes /
	// node_cpu_seconds_total filtered by `instance=~"<internal IP>.*"`, and this
	// enricher only ever knows the node NAME. With no IP that regex matches every
	// instance, so the card reported the whole cluster against one node's
	// allocatable: 51.3 GB used of 12.9 GB, and 20.0 cores of 3.92, measured on dev.
	//
	// Summing the per-container series is what the PromQL this replaces did and needs
	// no IP. It sums EVERY container the layer returned for the node, not the fifteen
	// the card lists: the total is what the node is using, and trimming it to the
	// visible rows would under-report it on any node running more than fifteen
	// containers — which is most of them. The rows therefore do not add up to the
	// total, by design.
	nodeUsed := sumLatestValues(results["top_pods"])
	nodeAlloc := firstLatestValue(results["node_alloc"])
	nodeCPUUsed := sumLatestValues(results["top_pods_cpu"])
	nodeCPUCapacity := firstLatestValue(results["node_cpu_alloc"])

	// Every query came back empty, allocatable included. A real node always
	// reports allocatable, so this is a node Prometheus has never heard of —
	// a name we guessed wrong — not an idle one. Rendering it would put a card
	// on the event saying the machine has no usage and no neighbours, which is
	// a claim we have not earned; failing leaves the event without the card,
	// which is what it had before.
	if len(neighbours) == 0 && len(cpuNeighbours) == 0 && nodeAlloc == 0 && nodeCPUCapacity == 0 {
		return nil, fmt.Errorf("noisy_neighbours_enricher: no metrics for node %q — it does not look like a node this cluster's Prometheus knows", nodeName)
	}

	return &noisyNeighbourData{
		NodeName:        nodeName,
		NodeUsed:        nodeUsed,
		NodeAllocatable: nodeAlloc,
		TotalRequested:  totalRequested,
		Neighbours:      neighbours,
		CPUMeasured:     cpuMeasured,
		NodeCPUUsed:     nodeCPUUsed,
		NodeCPUCapacity: nodeCPUCapacity,
		CPUNeighbours:   cpuNeighbours,
	}, nil
}

// noisyNeighboursResponse renders the card. Both providers go through it so the payload
// the UI consumes cannot drift between them — the fields below are consumed verbatim by
// the NoisyNeighbour card, and a missing `name` or `memory_requested` renders as
// "Container undefined does not have a memory requests".
func noisyNeighboursResponse(podName, namespace string, rawParams map[string]any, d *noisyNeighbourData) (playbooks.PlaybookActionResponse, error) {
	data := map[string]any{
		"node_name":          d.NodeName,
		"memory_used":        d.NodeUsed,
		"memory_allocatable": d.NodeAllocatable,
		"memory_requested":   d.TotalRequested,
		"total_pods":         len(d.Neighbours),
		"neighbours":         d.Neighbours,
	}
	// Only when we actually measured CPU. On a provider that cannot report it,
	// omitting the keys says "unknown"; a zero would say "nothing is using CPU",
	// which is a different and much more misleading claim.
	if d.CPUMeasured {
		data["cpu_used"] = d.NodeCPUUsed
		data["cpu_allocatable"] = d.NodeCPUCapacity
		data["cpu_neighbours"] = d.CPUNeighbours
	}
	payload := map[string]any{
		"name": "noisy_neighbours",
		"data": data,
	}

	additionalInfo := map[string]any{
		"title":              "Noisy Neighbours",
		"action_name":        "noisy_neighbours_enricher",
		"actual_action_name": "noisy_neighbours_enricher",
		"node_name":          d.NodeName,
		"pod_name":           podName,
		"namespace":          namespace,
	}
	metadata := map[string]any{
		"query-result-version": "1.0",
		"query":                rawParams,
	}
	return playbooks.NewPlaybookActionResponseJson(payload, additionalInfo, []playbooks.PlaybookActionResponseInsight{}, metadata), nil
}

// latestSample is one series reduced to its most recent sample.
type latestSample struct {
	Metric map[string]string
	Value  float64
}

// latestValuesByKey reduces a range result to {metric, latest value} per series,
// keyed by query — what the neighbour tables are built from.
func latestValuesByKey(output OutputMetricQuery) map[string][]latestSample {
	out := make(map[string][]latestSample, len(output.Results))
	for _, result := range output.Results {
		samples := make([]latestSample, 0, len(result.Payload))
		for _, series := range result.Payload {
			if len(series.Values) == 0 {
				continue
			}
			samples = append(samples, latestSample{Metric: series.Metric, Value: series.Values[len(series.Values)-1]})
		}
		out[result.QueryKey] = samples
	}
	return out
}

// indexByPodContainer builds a {namespace/pod/container → latest value} map from
// a kube-state-metrics result, for the O(1) requests/limits join.
func indexByPodContainer(samples []latestSample) map[string]float64 {
	out := map[string]float64{}
	for _, s := range samples {
		pod, container := s.Metric["pod"], s.Metric["container"]
		if pod == "" || container == "" {
			continue
		}
		out[s.Metric["namespace"]+"/"+pod+"/"+container] = s.Value
	}
	return out
}

// firstLatestValue is the latest sample of the first series — for the
// scalar-ish node queries that resolve to a single series.
// sumLatestValues totals one key's series — the node's own usage, summed from the
// containers running on it.
func sumLatestValues(samples []latestSample) float64 {
	total := 0.0
	for _, s := range samples {
		total += s.Value
	}
	return total
}

func firstLatestValue(samples []latestSample) float64 {
	for _, s := range samples {
		return s.Value
	}
	return 0
}
