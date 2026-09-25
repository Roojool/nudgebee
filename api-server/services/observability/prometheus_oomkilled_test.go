package observability

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The limit a container held when it was OOM-killed is the one input that lets
// rightsizing raise a memory limit rather than shrink it toward the observed peak.
// It is not a usage series: kube-state-metrics publishes the limit and the
// terminated reason as separate series, and only their join answers the question.
func TestBuildPrometheusWorkloadQueries_OOMKilledLimitJoinsLimitsWithLastTerminatedReason(t *testing.T) {
	max, err := parseAggregate("max")
	require.NoError(t, err)

	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "api", ContainerName: "api",
		Aggregate: max, RangeWindow: "14d", Step: "75s", RateWindow: "75s",
	}
	q := buildPrometheusWorkloadQueries(meta, []string{"container_oomkilled_limit_by_pod"})["container_oomkilled_limit_by_pod"]
	require.NotEmpty(t, q, "the key must be built, or rightsizing silently loses OOM-aware sizing")

	assert.Contains(t, q, "kube_pod_container_resource_limits", "the value reported IS the limit")
	assert.Contains(t, q, `resource="memory"`, "the cpu limit is a different number in the same metric")
	assert.Contains(t, q, "kube_pod_container_status_last_terminated_reason")
	assert.Contains(t, q, `reason="OOMKilled"`,
		"without this the query reports a limit for every container that ever terminated")
	assert.Contains(t, q, "group_left(reason)")

	// The join keys are the contract. `job` because both halves come from
	// kube-state-metrics and carry it; `namespace` because two namespaces running a
	// same-named pod would otherwise cross-join.
	assert.Contains(t, q, "on (namespace, container, pod, job)")
	assert.Contains(t, q, "max by (namespace, container, pod) (",
		"the answer is one series per container, carrying the canonical labels")

	// cAdvisor's two-armed node/instance form must not appear: neither half of this
	// join is a cAdvisor series, so there is no per-scraper duplication to collapse.
	assert.NotContains(t, q, "instance=", "kube-state-metrics has no instance arm to prefer")

	assert.True(t, strings.HasPrefix(q, "max_over_time("),
		"the loader this replaces took the max over the window; got %s", q)
	assert.Contains(t, q, "[14d:75s]", "over the requested window, at the requested step")
}

// Both halves of the join must be narrowed to the same workload. A filter applied to
// only one side reads as "this workload's limit, joined against ANY container's kill",
// which would report an OOM bump for a workload that was never killed.
func TestBuildPrometheusWorkloadQueries_OOMKilledLimitScopesBothHalves(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "api", ContainerName: "api",
		RangeWindow: "14d", Step: "75s", RateWindow: "75s",
	}
	q := buildPrometheusWorkloadQueries(meta, []string{"container_oomkilled_limit_by_pod"})["container_oomkilled_limit_by_pod"]

	assert.Equal(t, 2, strings.Count(q, `namespace="shop"`), "both halves narrowed to the namespace")
	assert.Equal(t, 2, strings.Count(q, `container="api"`), "both halves narrowed to the container")
	assert.Equal(t, 2, strings.Count(q, `pod=~"api-.*"`), "both halves narrowed to the workload's pods")
}

// An exact pod list is how rightsizing asks about the incarnations it is sizing. It
// has to reach both halves too, and the "resolved to no pods" sentinel must still
// match nothing rather than falling back to the workload prefix.
func TestBuildPrometheusWorkloadQueries_OOMKilledLimitHonoursThePodList(t *testing.T) {
	meta := RequestMetadata{
		Kind: "deployment", Namespace: "shop", Name: "api", ContainerName: "api",
		PodNames: []string{"api-7d9f", "api-3b2c"}, PodNamesSupplied: true,
		RangeWindow: "14d", Step: "75s", RateWindow: "75s",
	}
	q := buildPrometheusWorkloadQueries(meta, []string{"container_oomkilled_limit_by_pod"})["container_oomkilled_limit_by_pod"]
	assert.Equal(t, 2, strings.Count(q, `pod=~"api-7d9f|api-3b2c"`))

	meta.PodNames = []string{}
	empty := buildPrometheusWorkloadQueries(meta, []string{"container_oomkilled_limit_by_pod"})["container_oomkilled_limit_by_pod"]
	assert.Equal(t, 2, strings.Count(empty, "__no_pods_resolved__"),
		"an empty resolved list is an answer, not a reason to size on the neighbours' pods")
}

// Silence and "no such metric" are the same value to a caller reading a payload. The
// note is what lets rightsizing tell a workload that was never OOM-killed from a
// provider that cannot answer the question at all.
func TestDatadogOOMKilledLimit_ComesBackWithAReasonNotSilence(t *testing.T) {
	reason, unanswerable := datadogUnanswerableKeys["container_oomkilled_limit_by_pod"]
	require.True(t, unanswerable, "Datadog must say why, not return an empty payload")
	assert.Contains(t, reason, "terminated reason")

	results := datadogNoteUnanswerableKeys(nil, []string{"container_oomkilled_limit_by_pod"})
	require.Len(t, results, 1)
	assert.Equal(t, "container_oomkilled_limit_by_pod", results[0].QueryKey)
	assert.Empty(t, results[0].Payload, "empty, so nothing reads it as a measured limit")
	assert.NotEmpty(t, results[0].Note)
}

// Elasticsearch answers the same way, by falling through rather than by substituting
// a number that looks like one. Pinned because the fall-through is an absence, and an
// absence is exactly what a later edit would restore by accident.
func TestESOOMKilledLimit_IsNotAnsweredWithSomethingElse(t *testing.T) {
	for _, scope := range []esUtilScope{esScopeWorkload, esScopeNode, esScopeCluster} {
		_, ok := esUtilisationMetric("container_oomkilled_limit_by_pod", scope, false)
		assert.False(t, ok, "no Elasticsearch layout carries a per-container terminated reason")
	}
	assert.NotEmpty(t, esUtilUnsupportedNote("container_oomkilled_limit_by_pod", esScopeWorkload))
}
