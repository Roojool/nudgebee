package observability

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The per-container key family, at every scope it answers. The point of the family is
// that the ANSWER shape does not change with the scope — only the narrowing does — so
// a consumer that can read one can read all three.
var perContainerKeys = []string{
	"container_cpu_usage_by_pod",
	"container_memory_working_set_by_pod",
	"container_memory_max_usage_by_pod",
	"container_cpu_request_by_pod",
	"container_cpu_limit_by_pod",
	"container_memory_request_by_pod",
	"container_memory_limit_by_pod",
}

func scopedMeta(kind, namespace, name, node string) RequestMetadata {
	return RequestMetadata{
		Kind: kind, Namespace: namespace, Name: name, NodeName: node,
		RangeWindow: "14d", Step: "75s", RateWindow: "75s",
	}
}

func buildForScope(meta RequestMetadata, keys []string) map[string]string {
	if meta.Kind == "node" {
		return buildPrometheusNodeQueries(meta, keys)
	}
	return buildPrometheusWorkloadQueries(meta, keys)
}

// Every key, at every scope, is a legal query. Golden strings pin the text but cannot
// tell a valid query from one with a stray comma — and the cluster scope, where the
// filter is empty, is exactly where a stray comma appears.
//
// Cluster scope is exercised here because the Prometheus builder renders it, but this
// PR does NOT claim cluster scope as a supported scope for the family: Datadog returns
// nothing there (its workload builder bails on an empty filter) and Elasticsearch drops
// the namespace label. Its only consumer is the deferred cost-allocation pair, so it is
// left to the PR that needs it rather than shipped half-answered.
func TestPerContainerKeys_ParseAtEveryScope(t *testing.T) {
	scopes := map[string]RequestMetadata{
		"workload":  scopedMeta("deployment", "shop", "api", ""),
		"namespace": scopedMeta("", "shop", "", ""),
		"cluster":   scopedMeta("", "", "", ""),
		"node":      scopedMeta("node", "", "", "ip-10-0-1-5"),
	}
	aggregates := []string{"", "max", "p95", "count"}

	for scope, meta := range scopes {
		for _, agg := range aggregates {
			t.Run(fmt.Sprintf("%s/%s", scope, agg), func(t *testing.T) {
				m := meta
				parsed, err := parseAggregate(agg)
				require.NoError(t, err)
				m.Aggregate = parsed

				for key, query := range buildForScope(m, perContainerKeys) {
					_, parseErr := promQLParser.ParseExpr(stripClusterPlaceholder(query))
					require.NoError(t, parseErr, "%s at %s scope: %s", key, scope, query)
				}
			})
		}
	}
}

// Every per-container series carries namespace, pod and container, whatever scope
// produced it. Without namespace a cluster- or node-scoped answer cannot be joined
// back to a workload, and two namespaces' identically-named pods collide.
func TestPerContainerKeys_CarryNamespacePodContainer(t *testing.T) {
	// Workload and node are the scopes this PR claims; see the note above on cluster.
	for scope, meta := range map[string]RequestMetadata{
		"workload": scopedMeta("deployment", "shop", "api", ""),
		"node":     scopedMeta("node", "", "", "ip-10-0-1-5"),
	} {
		for key, query := range buildForScope(meta, perContainerKeys) {
			assert.Regexp(t, `by \((namespace, container, pod|namespace, pod, container)\)`, query,
				"%s at %s scope groups by all three", key, scope)
		}
	}
}

// Node scope asks both label conventions and unions them. Aggregating BEFORE the `or`
// is what makes the union safe: `or` is a set union on label sets, so once both arms
// group to the same series a container present in both is taken once, not twice.
func TestPerContainerKeys_NodeScopeAggregatesBeforeOr(t *testing.T) {
	q := buildPrometheusNodeQueries(scopedMeta("node", "", "", "ip-10-0-1-5"),
		[]string{"container_memory_working_set_by_pod"})["container_memory_working_set_by_pod"]

	assert.Contains(t, q, `node="ip-10-0-1-5"`)
	assert.Contains(t, q, `instance="ip-10-0-1-5"`)
	assert.Equal(t, 2, countOccurrences(q, "max by (namespace, pod, container)"),
		"both arms aggregate before the or: %s", q)
	// max, not sum: a cluster with two cAdvisor scrapers emits each container twice.
	assert.NotContains(t, q, "sum by (namespace, pod, container)")
}

// kube-state-metrics labels by node only, so the spec keys need no instance arm — and
// adding one would be a selector that never matches.
func TestPerContainerKeys_NodeScopeSpecFiltersByNodeOnly(t *testing.T) {
	q := buildPrometheusNodeQueries(scopedMeta("node", "", "", "ip-10-0-1-5"),
		[]string{"container_memory_request_by_pod"})["container_memory_request_by_pod"]

	assert.Contains(t, q, `kube_pod_container_resource_requests{`)
	assert.Contains(t, q, `resource="memory"`)
	assert.Contains(t, q, `node="ip-10-0-1-5"`)
	assert.NotContains(t, q, `instance=`)
}

// The denominator a caller needs to turn per-container usage into a percentage of the
// node. The workload builder answers these cluster-wide; at node scope they were absent.
func TestNodeScope_AllocatableKeys(t *testing.T) {
	q := buildPrometheusNodeQueries(scopedMeta("node", "", "", "ip-10-0-1-5"),
		[]string{"cpu_total", "mem_total"})

	assert.Contains(t, q["cpu_total"], `kube_node_status_allocatable{`)
	assert.Contains(t, q["cpu_total"], `resource="cpu"`)
	assert.Contains(t, q["mem_total"], `resource="memory"`)
	for k, v := range q {
		assert.Contains(t, v, `node="ip-10-0-1-5"`, k)
		// One series for the node. A cluster running two kube-state-metrics emits
		// this once per instance, each carrying that instance's own namespace/pod
		// labels — observed on dev, where it returned two series from the opencost
		// and victoria namespaces. Anything summing them reads double the capacity.
		assert.Contains(t, v, `max by (node)`, "%s must collapse duplicate kube-state-metrics series", k)
	}
}

// The live 2x bug: cAdvisor emits a pod-level cgroup rollup series with an empty
// container label alongside the per-container ones, so a namespace row that did not
// exclude it counted every container twice.
func TestNamespaceScope_ExcludesPodCgroupRollup(t *testing.T) {
	q := buildPrometheusWorkloadQueries(scopedMeta("", "shop", "", ""),
		[]string{"memory_usage", "cpu_usage"})

	for key, query := range q {
		assert.Contains(t, query, `container!=""`, "%s must exclude the pod-level rollup", key)
	}

	// A named container still pins to that container rather than excluding empties.
	named := scopedMeta("", "shop", "", "")
	named.ContainerName = "api"
	got := buildPrometheusWorkloadQueries(named, []string{"memory_usage"})["memory_usage"]
	assert.Contains(t, got, `container="api"`)
}

// promSelector is what keeps a key renderable at cluster scope, where the filter is
// empty. Both edges — empty filter, trailing comma — otherwise emit a stray comma that
// Prometheus rejects outright.
func TestPromSelector_TolerdatesEmptyAndTrailingComma(t *testing.T) {
	assert.Equal(t, `resource="cpu"`, promSelector("", `resource="cpu"`))
	assert.Equal(t, `resource="cpu"`, promSelector("   ", `resource="cpu"`))
	assert.Equal(t, `namespace="shop", resource="cpu"`, promSelector(` namespace="shop",`, `resource="cpu"`))
	assert.Equal(t, `namespace="shop"`, promSelector(` namespace="shop"`))
	assert.Equal(t, "", promSelector(""))
}
