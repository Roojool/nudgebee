package observability

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func perPodMeta(t *testing.T, aggregate string) RequestMetadata {
	t.Helper()
	parsed, err := parseAggregate(aggregate)
	require.NoError(t, err)
	return RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "api", ContainerName: "api",
		RangeWindow: "14d", Step: "75s", RateWindow: "75s",
		Aggregate: parsed,
	}
}

// Golden strings, per this file's convention: a template change is caught here rather
// than re-derived. This is the query rightsizing sends for one CPU percentile.
func TestBuildPrometheusWorkloadQueries_PerPodCPUPercentile(t *testing.T) {
	queries := buildPrometheusWorkloadQueries(perPodMeta(t, "p92"), []string{"container_cpu_usage_by_pod"})

	assert.Equal(t,
		`quantile_over_time(0.92, (max by (container, pod) (rate(container_cpu_usage_seconds_total{__CLUSTER__  namespace="shop", pod=~"api-.*", container="api", job="kubelet"}[75s])) or max by (container, pod) (rate(container_cpu_usage_seconds_total{__CLUSTER__  namespace="shop", pod=~"api-.*", container="api"}[75s])))[14d:75s])`,
		queries["container_cpu_usage_by_pod"])
}

// Without an aggregate the same key is an ordinary time series, so it can back a
// chart as well as a recommendation.
func TestBuildPrometheusWorkloadQueries_PerPodWithoutAggregateIsASeries(t *testing.T) {
	queries := buildPrometheusWorkloadQueries(perPodMeta(t, ""), []string{"container_memory_working_set_by_pod"})

	got := queries["container_memory_working_set_by_pod"]
	assert.NotContains(t, got, "_over_time(", "no collapse was asked for")
	assert.Contains(t, got, `max by (container, pod) (container_memory_working_set_bytes{`)
}

// The 2x bug this work already chased once: a cluster running two cAdvisor scrapers
// reports the same container twice under different `job` labels. The query prefers
// the kubelet copy and falls back for clusters that label it otherwise — so the
// dedup lives in the query, where it is a property of the Prometheus deployment,
// rather than in a consumer.
func TestBuildPrometheusWorkloadQueries_PerPodPrefersKubeletSeries(t *testing.T) {
	for _, key := range []string{
		"container_cpu_usage_by_pod",
		"container_memory_working_set_by_pod",
		"container_memory_max_usage_by_pod",
	} {
		t.Run(key, func(t *testing.T) {
			got := buildPrometheusWorkloadQueries(perPodMeta(t, "max"), []string{key})[key]
			require.NotEmpty(t, got)
			assert.Equal(t, 1, strings.Count(got, ` or `), "exactly one fallback arm")
			assert.Contains(t, got, `job="kubelet"`, "the kubelet copy is preferred")
			// Both arms group the same way, or the fallback would answer a different
			// question than the arm it replaces.
			assert.Equal(t, 2, strings.Count(got, `max by (container, pod)`))
		})
	}
}

// An exact pod list beats the name prefix, which is wrong twice over: it misses pods
// that no longer exist, and "api" also matches "api-worker".
func TestBuildPrometheusWorkloadQueries_PerPodUsesTheSuppliedPodList(t *testing.T) {
	meta := perPodMeta(t, "p99")
	meta.PodNames, meta.PodNamesSupplied = []string{"api-7d9f", "api-3b2c"}, true

	got := buildPrometheusWorkloadQueries(meta, []string{"container_cpu_usage_by_pod"})["container_cpu_usage_by_pod"]
	assert.Contains(t, got, `pod=~"api-7d9f|api-3b2c"`)
	assert.NotContains(t, got, `pod=~"api-.*"`, "the prefix would also match api-worker")
}

// An empty list is a real answer — the caller resolved the pods and there are none —
// so it must match nothing. Falling back to the prefix here would size this workload
// on its neighbours' pods.
func TestBuildPrometheusWorkloadQueries_EmptyPodListMatchesNothing(t *testing.T) {
	meta := perPodMeta(t, "p99")
	meta.PodNames, meta.PodNamesSupplied = []string{}, true

	got := buildPrometheusWorkloadQueries(meta, []string{"container_cpu_usage_by_pod"})["container_cpu_usage_by_pod"]
	// Not a regex: Prometheus anchors matchers, and `pod=~"$^"` still matches the
	// EMPTY string, which cAdvisor emits for machine-level cgroups. An underscore
	// cannot appear in an RFC1123 pod name, so this matches nothing, always.
	assert.Contains(t, got, `pod="__no_pods_resolved__"`)
	assert.NotContains(t, got, `pod=~"api-.*"`)
}

// The contract's central promise: a request that names no aggregate and no pod list
// produces exactly the query it produced before any of this existed.
func TestBuildPrometheusWorkloadQueries_ExistingKeysAreUnchanged(t *testing.T) {
	plain := RequestMetadata{Kind: "deployment", Namespace: "shop", Name: "api"}
	withNewFields := plain
	// Aggregate deliberately left zero: it applies to the keys that ask for it.
	withNewFields.PodNames, withNewFields.PodNamesSupplied = nil, false

	keys := []string{"cpu_usage", "memory_usage", "cpu_request", "cpu_limit", "memory_request", "memory_limit", "pod_count"}
	before := buildPrometheusWorkloadQueries(plain, keys)
	after := buildPrometheusWorkloadQueries(withNewFields, keys)

	require.Equal(t, len(keys), len(after), "every key still answers")
	assert.Equal(t, before, after)
}
