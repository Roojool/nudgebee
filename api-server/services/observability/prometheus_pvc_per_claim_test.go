package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pvcKey(t *testing.T, key string, meta RequestMetadata) string {
	t.Helper()
	meta.RangeWindow, meta.Step, meta.RateWindow = "7d", "5m", "5m"
	q := buildPrometheusWorkloadQueries(meta, []string{key})[key]
	require.NotEmpty(t, q, "%s must be built", key)
	_, err := promQLParser.ParseExpr(stripClusterPlaceholder(q))
	require.NoError(t, err, "%s must be valid PromQL:\n%s", key, q)
	return q
}

// Volume rightsizing sizes each claim on its own and keys on (claim, namespace). A
// summed answer tells it nothing, and a claim name alone is ambiguous across
// namespaces.
func TestPVCPerClaimKeys_AreOneSeriesPerClaimWithBothLabels(t *testing.T) {
	for _, key := range []string{"pvc_used_by_claim", "pvc_capacity_by_claim"} {
		q := pvcKey(t, key, RequestMetadata{Kind: "deployment"})
		assert.Contains(t, q, "max by (namespace, persistentvolumeclaim)", key)
		assert.NotContains(t, q, "sum(", "%s must not collapse the claims together", key)
	}
}

// Capacity is what the volume HAS; pvc_requests is what was asked for. A resize
// decision compares usage against the former, and the two differ whenever a storage
// class rounds up or a volume has already been expanded.
func TestPVCCapacityByClaim_ReadsTheVolumeNotTheRequest(t *testing.T) {
	q := pvcKey(t, "pvc_capacity_by_claim", RequestMetadata{Kind: "deployment"})
	assert.Contains(t, q, "kubelet_volume_stats_capacity_bytes")
	assert.NotContains(t, q, "kube_persistentvolumeclaim_resource_requests_storage_bytes")

	// The summing key keeps answering the other question.
	old := buildPrometheusWorkloadQueries(RequestMetadata{Kind: "deployment", Namespace: "shop"}, []string{"pvc_requests"})["pvc_requests"]
	assert.Contains(t, old, "kube_persistentvolumeclaim_resource_requests_storage_bytes")
}

// The consumer walks every claim in the cluster, so no namespace is a real scope.
func TestPVCPerClaimKeys_AnswerClusterWide(t *testing.T) {
	q := pvcKey(t, "pvc_used_by_claim", RequestMetadata{Kind: "deployment"})
	assert.NotContains(t, q, `namespace="`, "an unscoped request must not invent a namespace")

	scoped := pvcKey(t, "pvc_used_by_claim", RequestMetadata{Kind: "deployment", Namespace: "shop"})
	assert.Contains(t, scoped, `namespace="shop"`)
}

// "current" is a max over ten minutes and "peak" a max over seven days: same key,
// different window, which is what keeps this two keys rather than four.
func TestPVCPerClaimKeys_HonourTheAggregate(t *testing.T) {
	max, err := parseAggregate("max")
	require.NoError(t, err)

	plain := pvcKey(t, "pvc_used_by_claim", RequestMetadata{Kind: "deployment"})
	assert.NotContains(t, plain, "max_over_time", "no aggregate means the time series")

	collapsed := pvcKey(t, "pvc_used_by_claim", RequestMetadata{Kind: "deployment", Aggregate: max})
	assert.Contains(t, collapsed, "max_over_time(")
	assert.Contains(t, collapsed, "[7d:5m]")
}

func TestDatadogPVCPerClaimKeys_GroupByClaimAndNamespace(t *testing.T) {
	q := buildDatadogWorkloadQueries(RequestMetadata{Kind: "deployment"},
		[]string{"pvc_used_by_claim", "pvc_capacity_by_claim"})

	assert.Contains(t, q["pvc_used_by_claim"], "kubernetes.kubelet.volume.stats.used_bytes")
	assert.Contains(t, q["pvc_capacity_by_claim"], "kubernetes.kubelet.volume.stats.capacity_bytes")
	for key, v := range q {
		assert.Contains(t, v, "by {persistentvolumeclaim,kube_namespace}", key)
		assert.Contains(t, v, "{*}", "%s: no scope means every claim, not none", key)
	}

	scoped := buildDatadogWorkloadQueries(RequestMetadata{Kind: "deployment", Namespace: "shop"},
		[]string{"pvc_used_by_claim"})["pvc_used_by_claim"]
	assert.Contains(t, scoped, "kube_namespace:shop")
}

// The claim level must carry a name, or the series comes back unidentifiable — which
// is harder to spot than a wrong number.
func TestESPVCPerClaimKeys_LabelTheClaim(t *testing.T) {
	for _, key := range []string{"pvc_used_by_claim", "pvc_capacity_by_claim"} {
		plan, ok := esUtilisationMetric(key, esScopeWorkload, false)
		require.True(t, ok, "%s must be answerable on Elasticsearch", key)
		require.Len(t, plan.Sources, 1)

		src := plan.Sources[0]
		require.True(t, src.PVCScoped, "%s: pod and node filters do not apply to a claim", key)
		require.Equal(t, []string{src.Fields.Namespace, src.Fields.PVC}, src.GroupBy,
			"%s: namespace above the claim", key)
		assert.Equal(t, "namespace", esPerPodLevelLabel(src, src.Fields.Namespace))
		assert.Equal(t, "persistentvolumeclaim", esPerPodLevelLabel(src, src.Fields.PVC))
	}
}
