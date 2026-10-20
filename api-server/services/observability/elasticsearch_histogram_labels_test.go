package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A key answers with the same labels whether or not an aggregate was requested.
//
// They disagreed, and only the collapsed half was right. The histogram half named its
// two grouping levels "pod" and "container" positionally, which holds only while every
// per-pod source groups by exactly those. pvc_used_by_claim groups by namespace and
// claim, so it answered with pod=<namespace>, container=<claim>: the right values under
// names nothing looks for. Volume rightsizing reads `persistentvolumeclaim`, found none,
// and produced no recommendation for any volume on an Elasticsearch account — with the
// data present and correct in the response the whole time.
func TestESPerPodLabels_HistogramAndCollapsedAgree(t *testing.T) {
	for _, key := range []string{
		"container_cpu_usage_by_pod",
		"container_memory_working_set_by_pod",
		"pvc_used_by_claim",
		"pvc_capacity_by_claim",
	} {
		for _, scope := range []esUtilScope{esScopeWorkload, esScopeCluster} {
			m, ok := esUtilisationMetric(key, scope, false)
			if !ok {
				continue
			}
			for _, src := range m.Sources {
				if len(src.GroupBy) == 0 {
					continue
				}
				got := esPerPodHistogramLabels(src, "lvl0", "lvl1")

				// The histogram response carries two grouping levels, so only the
				// outer two are comparable; a source with three loses the innermost
				// here, which it did before this change too.
				for i, value := range []string{"lvl0", "lvl1"} {
					if i >= len(src.GroupBy) {
						break
					}
					name := esPerPodLevelLabel(src, src.GroupBy[i])
					if name == "" {
						continue
					}
					assert.Equalf(t, value, got[name],
						"%s/%s: level %d is grouped by %q so it must be labelled %q",
						key, scope, i, src.GroupBy[i], name)
				}

				// And nothing is labelled by position. A level is named "pod" only
				// when the source really grouped by the pod field.
				if pod, ok := got["pod"]; ok {
					idx := 0
					if pod == "lvl1" {
						idx = 1
					}
					assert.Equalf(t, src.Fields.Pod, src.GroupBy[idx],
						"%s/%s labelled level %d 'pod' but it groups by %q",
						key, scope, idx, src.GroupBy[idx])
				}
			}
		}
	}
}

// The claim keys specifically: the labels volume rightsizing reads must be there.
func TestESPVCHistogramLabels_CarryNamespaceAndClaim(t *testing.T) {
	m, ok := esUtilisationMetric("pvc_used_by_claim", esScopeCluster, false)
	require.True(t, ok, "pvc_used_by_claim must be answered at cluster scope")
	require.NotEmpty(t, m.Sources)

	got := esPerPodHistogramLabels(m.Sources[0], "shop", "data-postgresql-0")
	assert.Equal(t, "shop", got["namespace"])
	assert.Equal(t, "data-postgresql-0", got["persistentvolumeclaim"],
		"the consumer keys on this label; without it every claim reads as zero usage")
	assert.NotContains(t, got, "pod", "a namespace is not a pod")
	assert.NotContains(t, got, "container", "a claim is not a container")
}

// And the pod keys keep the labels they always had — this is the case the positional
// naming got right, so the fix must not move it.
func TestESPodHistogramLabels_StillNamePods(t *testing.T) {
	m, ok := esUtilisationMetric("container_cpu_usage_by_pod", esScopeWorkload, false)
	require.True(t, ok)
	require.NotEmpty(t, m.Sources)

	for _, src := range m.Sources {
		if len(src.GroupBy) == 0 || esPerPodLevelLabel(src, src.GroupBy[0]) != "pod" {
			continue
		}
		got := esPerPodHistogramLabels(src, "api-7d9f-abc", "api")
		assert.Equal(t, "api-7d9f-abc", got["pod"])
		return
	}
	t.Skip("no source groups by pod first at this scope")
}
