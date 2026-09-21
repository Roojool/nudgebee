package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"nudgebee/services/security"
)

// Elastic Agent field paths for the per-container memory picture this card needs. As
// elsewhere in the ES metric mapping, the field path IS the metric identity, so an
// `exists` on the value field selects the right metricset without a term on
// `metricset.name` (which a customer template may map as `text`, where a term matches
// nothing).
const (
	esNNContainerWorkingSet = "kubernetes.container.memory.workingset.bytes"
	esNNContainerRequest    = "kubernetes.container.memory.request.bytes"
	esNNContainerLimit      = "kubernetes.container.memory.limit.bytes"
	esNNNodeAllocatable     = "kubernetes.node.memory.allocatable.bytes"

	// CPU counterparts. Usage sits in the same kubernetes.container documents as working
	// set, so it rides on the memory aggregation at no extra query; requests, limits and
	// allocatable come from the same state metricsets as their memory twins.
	esNNContainerCPUUsage   = "kubernetes.container.cpu.usage.nanocores"
	esNNContainerCPURequest = "kubernetes.container.cpu.request.cores"
	esNNContainerCPULimit   = "kubernetes.container.cpu.limit.cores"
	esNNNodeCPUAllocatable  = "kubernetes.node.cpu.allocatable.cores"
)

// esNoisyNeighbours builds the noisy-neighbours payload from Elasticsearch.
//
// The PromQL form needs five queries and a three-way guess at where the node name lands
// (`node`, `instance`, or both, depending on the scrape config). ECS has one spelling of
// the node name, so this is two searches: the per-container picture on the node, and the
// node's allocatable memory and CPU. CPU usage is read from the same documents as memory,
// so the CPU half of the card costs no extra query.
//
// Requests and limits are a SECOND search, keyed by the pods the first one found.
// They live in the state_container metricset, which carries no node field — verified
// against a live cluster, where asking for them alongside working set returns null for
// every container. Since that metricset cannot be narrowed by node, the pods on the node
// are resolved first and used as the filter, which is both correct and bounded.
func esNoisyNeighbours(ctx *security.RequestContext, accountID, nodeName string, lookbackMinutes, topN int) (*noisyNeighbourData, error) {
	// Guarded here rather than at the call site so every caller is covered: without a
	// node there is nothing to narrow by, and querying anyway would return the whole
	// cluster's containers as though they were this node's.
	if nodeName == "" {
		return nil, errors.New("noisy neighbours: node name required")
	}
	cfg, err := GetElasticsearchConfig(ctx, accountID)
	if err != nil {
		return nil, err
	}
	index := "metrics-*"
	if cfg.MetricsIndex != "" {
		index = cfg.MetricsIndex
	}

	timeRange := map[string]any{
		"range": map[string]any{
			"@timestamp": map[string]any{"gte": fmt.Sprintf("now-%dm", lookbackMinutes), "lte": "now"},
		},
	}
	nodeClause := esNodeNameClause("kubernetes.node.name", nodeName)

	containerFilters := []any{
		map[string]any{"exists": map[string]any{"field": esNNContainerWorkingSet}},
		timeRange,
	}
	if nodeClause != nil {
		containerFilters = append(containerFilters, nodeClause)
	}

	// max, not avg: this card answers "how much is this container holding", and an
	// average across the lookback understates a container that just grew — which is
	// exactly the one that pushed its neighbour into an OOM.
	body := map[string]any{
		"size":  0,
		"query": map[string]any{"bool": map[string]any{"filter": containerFilters}},
		"aggs": map[string]any{
			"pods": map[string]any{
				"terms": map[string]any{"field": "kubernetes.pod.name", "size": esUtilTermsSize},
				"aggs": map[string]any{
					"ns": map[string]any{"terms": map[string]any{"field": "kubernetes.namespace", "size": 1}},
					"containers": map[string]any{
						"terms": map[string]any{"field": "kubernetes.container.name", "size": esUtilTermsSize},
						"aggs": map[string]any{
							"used": map[string]any{"max": map[string]any{"field": esNNContainerWorkingSet}},
							// avg, not max: CPU is a rate, and the PromQL path ranks on a
							// five-minute rate — a short average. A max would rank a
							// container on its single busiest sample.
							"cpu_used": map[string]any{"avg": map[string]any{"field": esNNContainerCPUUsage}},
						},
					},
				},
			},
		},
	}

	var resp esNNResponse
	if err := esSearchInto(ctx, cfg, index, body, &resp); err != nil {
		return nil, err
	}

	// The pods this card found on the node ARE the join key for the spec lookup, so it
	// reuses them rather than issuing esPodsOnNode's query a second time.
	podNames := make([]string, 0, len(resp.Aggregations.Pods.Buckets))
	for _, pod := range resp.Aggregations.Pods.Buckets {
		podNames = append(podNames, pod.Key)
	}
	specs, err := esContainerSpecs(ctx, cfg, index, podNames, timeRange)
	if err != nil {
		// Usage alone still renders the rows; the card shows the request column empty.
		ctx.GetLogger().Warn("noisy_neighbours: container requests/limits unavailable",
			"account_id", accountID, "node", nodeName, "err", err)
		specs = map[string]esContainerSpec{}
	}

	out := buildESNoisyNeighbourData(nodeName, resp, specs, topN)

	if memAlloc, cpuAlloc, err := esNodeAllocatable(ctx, cfg, index, nodeClause, timeRange); err == nil {
		out.NodeAllocatable = memAlloc
		out.NodeCPUCapacity = cpuAlloc
	} else {
		// A missing allocatable leaves the gauge without its denominator, which the
		// card renders as an empty bar. Worth saying why rather than failing the whole
		// enricher — the neighbour rows above are still useful on their own.
		ctx.GetLogger().Warn("noisy_neighbours: node allocatable unavailable",
			"account_id", accountID, "node", nodeName, "err", err)
	}
	return out, nil
}

// esContainerSpecs reads memory and CPU requests and limits for named pods, keyed
// pod/container.
//
// Same shape as the node-scope pod join in the utilisation path (esPodsOnNode): the
// state_container metricset holds these two numbers and carries no node field, so the
// only way to ask "on this node" is to name the pods. This one takes the pod list as an
// argument because the caller already has it.
func esContainerSpecs(ctx *security.RequestContext, cfg *ElasticsearchConfig, index string, podNames []string, timeRange map[string]any) (map[string]esContainerSpec, error) {
	out := map[string]esContainerSpec{}
	if len(podNames) == 0 {
		return out, nil
	}
	body := map[string]any{
		"size": 0,
		"query": map[string]any{"bool": map[string]any{"filter": []any{
			// Either request is enough to select a state_container document: a container
			// may set a CPU request and no memory request, or the reverse.
			map[string]any{"bool": map[string]any{
				"should": []any{
					map[string]any{"exists": map[string]any{"field": esNNContainerRequest}},
					map[string]any{"exists": map[string]any{"field": esNNContainerCPURequest}},
				},
				"minimum_should_match": 1,
			}},
			timeRange,
			map[string]any{"terms": map[string]any{"kubernetes.pod.name": podNames}},
		}}},
		"aggs": map[string]any{
			"pods": map[string]any{
				"terms": map[string]any{"field": "kubernetes.pod.name", "size": esUtilTermsSize},
				"aggs": map[string]any{
					"containers": map[string]any{
						"terms": map[string]any{"field": "kubernetes.container.name", "size": esUtilTermsSize},
						"aggs": map[string]any{
							"requested":     map[string]any{"max": map[string]any{"field": esNNContainerRequest}},
							"limit":         map[string]any{"max": map[string]any{"field": esNNContainerLimit}},
							"cpu_requested": map[string]any{"max": map[string]any{"field": esNNContainerCPURequest}},
							"cpu_limit":     map[string]any{"max": map[string]any{"field": esNNContainerCPULimit}},
						},
					},
				},
			},
		},
	}
	var resp esNNSpecResponse
	if err := esSearchInto(ctx, cfg, index, body, &resp); err != nil {
		return nil, err
	}
	for _, pod := range resp.Aggregations.Pods.Buckets {
		for _, c := range pod.Containers.Buckets {
			out[pod.Key+"/"+c.Key] = esContainerSpec{
				Requested:    esNNValue(c.Requested.Value),
				Limit:        esNNValue(c.Limit.Value),
				CPURequested: esNNValue(c.CPURequested.Value),
				CPULimit:     esNNValue(c.CPULimit.Value),
			}
		}
	}
	return out, nil
}

// esContainerSpec holds one container's declared resources. Requested and Limit are
// memory in bytes; the CPU pair is in cores, as kube-state-metrics reports them.
type esContainerSpec struct {
	Requested    float64
	Limit        float64
	CPURequested float64
	CPULimit     float64
}

// esNodeAllocatable returns the node's allocatable memory (bytes) and CPU (cores) from
// one search. Either may be zero when the state_node metricset does not carry it.
func esNodeAllocatable(ctx *security.RequestContext, cfg *ElasticsearchConfig, index string, nodeClause map[string]any, timeRange map[string]any) (memory, cpu float64, err error) {
	filters := []any{
		map[string]any{"bool": map[string]any{
			"should": []any{
				map[string]any{"exists": map[string]any{"field": esNNNodeAllocatable}},
				map[string]any{"exists": map[string]any{"field": esNNNodeCPUAllocatable}},
			},
			"minimum_should_match": 1,
		}},
		timeRange,
	}
	if nodeClause != nil {
		filters = append(filters, nodeClause)
	}
	body := map[string]any{
		"size":  0,
		"query": map[string]any{"bool": map[string]any{"filter": filters}},
		"aggs": map[string]any{
			"alloc":     map[string]any{"max": map[string]any{"field": esNNNodeAllocatable}},
			"cpu_alloc": map[string]any{"max": map[string]any{"field": esNNNodeCPUAllocatable}},
		},
	}
	var resp struct {
		Aggregations struct {
			Alloc struct {
				Value *float64 `json:"value"`
			} `json:"alloc"`
			CPUAlloc struct {
				Value *float64 `json:"value"`
			} `json:"cpu_alloc"`
		} `json:"aggregations"`
	}
	if err := esSearchInto(ctx, cfg, index, body, &resp); err != nil {
		return 0, 0, err
	}
	return esNNValue(resp.Aggregations.Alloc.Value), esNNValue(resp.Aggregations.CPUAlloc.Value), nil
}

// buildESNoisyNeighbourData turns the per-container search and the spec lookup into the
// card's data. Pure so it can be tested from response fixtures.
//
// Both lists are ranked and cut independently — the container holding the most memory is
// usually not the one burning the most CPU — while each node total is summed over every
// container before the cut, so the gauge reflects the whole node when only topN rows show.
func buildESNoisyNeighbourData(nodeName string, resp esNNResponse, specs map[string]esContainerSpec, topN int) *noisyNeighbourData {
	// Neighbours starts empty rather than nil: a nil slice marshals to `null`, and the
	// card iterates it. An idle node legitimately has nothing to show and must render as
	// an empty list, not fail.
	out := &noisyNeighbourData{
		NodeName:      nodeName,
		Neighbours:    []map[string]any{},
		CPUNeighbours: []map[string]any{},
	}
	for _, pod := range resp.Aggregations.Pods.Buckets {
		namespace := ""
		if len(pod.NS.Buckets) > 0 {
			namespace = pod.NS.Buckets[0].Key
		}
		for _, c := range pod.Containers.Buckets {
			spec := specs[pod.Key+"/"+c.Key]
			if c.Used.Value != nil {
				used := *c.Used.Value
				out.NodeUsed += used
				out.TotalRequested += spec.Requested
				out.Neighbours = append(out.Neighbours, map[string]any{
					"name":             c.Key,
					"pod_name":         pod.Key,
					"namespace":        namespace,
					"node_name":        nodeName,
					"memory_used":      used,
					"memory_requested": spec.Requested,
					"memory_limit":     spec.Limit,
				})
			}
			if c.CPUUsed.Value != nil {
				cores := *c.CPUUsed.Value * nanocoresToCores
				out.CPUMeasured = true
				out.NodeCPUUsed += cores
				out.CPUNeighbours = append(out.CPUNeighbours, map[string]any{
					"name":          c.Key,
					"pod_name":      pod.Key,
					"namespace":     namespace,
					"node_name":     nodeName,
					"cpu_used":      cores,
					"cpu_requested": spec.CPURequested,
					"cpu_limit":     spec.CPULimit,
				})
			}
		}
	}
	out.Neighbours = topNeighboursBy(out.Neighbours, "memory_used", topN)
	out.CPUNeighbours = topNeighboursBy(out.CPUNeighbours, "cpu_used", topN)
	return out
}

// topNeighboursBy sorts rows by a float64 field, largest first, and keeps the first n.
func topNeighboursBy(rows []map[string]any, field string, n int) []map[string]any {
	sort.SliceStable(rows, func(i, j int) bool {
		vi, _ := rows[i][field].(float64)
		vj, _ := rows[j][field].(float64)
		return vi > vj
	})
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

// esSearchInto posts one _search and decodes it, so the two queries above do not each
// repeat the request/read/unmarshal dance.
func esSearchInto(ctx *security.RequestContext, cfg *ElasticsearchConfig, index string, body map[string]any, into any) error {
	url := fmt.Sprintf("%s/%s/_search", cfg.Url, index)
	resp, err := esRequestJSON("POST", url, body, cfg) //nolint:bodyclose
	if err != nil {
		return err
	}
	raw, err := readResponse(resp, "noisy neighbours query")
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func esNNValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
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

type esNNResponse struct {
	Aggregations struct {
		Pods struct {
			Buckets []struct {
				Key string `json:"key"`
				NS  struct {
					Buckets []struct {
						Key string `json:"key"`
					} `json:"buckets"`
				} `json:"ns"`
				Containers struct {
					Buckets []struct {
						Key  string `json:"key"`
						Used struct {
							Value *float64 `json:"value"`
						} `json:"used"`
						CPUUsed struct {
							Value *float64 `json:"value"`
						} `json:"cpu_used"`
					} `json:"buckets"`
				} `json:"containers"`
			} `json:"buckets"`
		} `json:"pods"`
	} `json:"aggregations"`
}

type esNNSpecResponse struct {
	Aggregations struct {
		Pods struct {
			Buckets []struct {
				Key        string `json:"key"`
				Containers struct {
					Buckets []struct {
						Key       string `json:"key"`
						Requested struct {
							Value *float64 `json:"value"`
						} `json:"requested"`
						Limit struct {
							Value *float64 `json:"value"`
						} `json:"limit"`
						CPURequested struct {
							Value *float64 `json:"value"`
						} `json:"cpu_requested"`
						CPULimit struct {
							Value *float64 `json:"value"`
						} `json:"cpu_limit"`
					} `json:"buckets"`
				} `json:"containers"`
			} `json:"buckets"`
		} `json:"pods"`
	} `json:"aggregations"`
}
