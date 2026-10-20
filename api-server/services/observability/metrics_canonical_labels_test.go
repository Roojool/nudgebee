package observability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of the whole mapper: the same question gets the same label name back,
// whichever provider answered it.
func TestCanonicaliseUtilisationLabels_SameDimensionSameName(t *testing.T) {
	datadog := []QueryResult{{QueryKey: "cpu_usage", Payload: []Result{{Metric: map[string]string{
		"pod_name": "api-7d9f", "kube_container_name": "api", "kube_namespace": "shop",
		"kube_deployment": "api", "host": "node-1", "unit": "core",
	}}}}}
	newrelic := []QueryResult{{QueryKey: "cpu_usage", Payload: []Result{{Metric: map[string]string{
		"podName": "api-7d9f", "containerName": "api", "namespaceName": "shop",
		"deploymentName": "api", "nodeName": "node-1",
	}}}}}
	// Prometheus already speaks canonically, so it must come back untouched.
	prometheus := []QueryResult{{QueryKey: "cpu_usage", Payload: []Result{{Metric: map[string]string{
		"pod": "api-7d9f", "container": "api", "namespace": "shop", "node": "node-1",
	}}}}}

	canonicaliseUtilisationLabels(datadog, "datadog")
	canonicaliseUtilisationLabels(newrelic, "newrelic")
	canonicaliseUtilisationLabels(prometheus, "prometheus")

	for _, tc := range []struct {
		name   string
		metric map[string]string
	}{
		{"datadog", datadog[0].Payload[0].Metric},
		{"newrelic", newrelic[0].Payload[0].Metric},
		{"prometheus", prometheus[0].Payload[0].Metric},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "api-7d9f", tc.metric[CanonicalPod])
			assert.Equal(t, "api", tc.metric[CanonicalContainer])
			assert.Equal(t, "shop", tc.metric[CanonicalNamespace])
			assert.Equal(t, "node-1", tc.metric[CanonicalNode])
		})
	}

	// Renamed, not duplicated: a legend built from the whole map stays one entry per
	// dimension.
	assert.NotContains(t, datadog[0].Payload[0].Metric, "pod_name")
	assert.NotContains(t, newrelic[0].Payload[0].Metric, "podName")
	assert.Equal(t, "api", datadog[0].Payload[0].Metric[CanonicalWorkload], "kube_deployment answers 'which workload'")
	// Labels nothing maps are left exactly as they arrived.
	assert.Equal(t, "core", datadog[0].Payload[0].Metric["unit"])
}

// destination_* names the CALLEE of a request, not the object queried. Three screens
// read those labels by name — KubernetesApplicationGrouping renders a row per
// destination, HttpLatencyTable keys its rows on them, the status chart reads the
// actual_ variant — so renaming them blanks those screens.
func TestCanonicaliseUtilisationLabels_LeavesDestinationLabelsAlone(t *testing.T) {
	results := []QueryResult{{QueryKey: "workload_http_error_rate", Payload: []Result{{Metric: map[string]string{
		"destination_workload_name":             "checkout",
		"destination_workload_namespace":        "shop",
		"actual_destination_workload_name":      "checkout",
		"actual_destination_workload_namespace": "shop",
		"pod_name":                              "api-7d9f",
	}}}}}

	canonicaliseUtilisationLabels(results, "datadog")

	m := results[0].Payload[0].Metric
	assert.Equal(t, "checkout", m["destination_workload_name"])
	assert.Equal(t, "shop", m["destination_workload_namespace"])
	assert.Equal(t, "checkout", m["actual_destination_workload_name"])
	assert.Equal(t, "shop", m["actual_destination_workload_namespace"])
	assert.NotContains(t, m, CanonicalWorkload, "callee identity is not the queried workload")
	assert.Equal(t, "api-7d9f", m[CanonicalPod], "the queried object is still canonicalised")
}

// A provider that already answers under the canonical name keeps its own value: the
// eBPF paths emit a real `workload`, and a tag that maps onto the same name must not
// silently replace it.
func TestCanonicaliseUtilisationLabels_NeverOverwrites(t *testing.T) {
	results := []QueryResult{{QueryKey: "cpu_usage", Payload: []Result{{Metric: map[string]string{
		"workload":        "the-real-one",
		"kube_deployment": "something-else",
		"pod":             "api-7d9f",
		"pod_name":        "api-7d9f",
	}}}}}

	canonicaliseUtilisationLabels(results, "datadog")

	m := results[0].Payload[0].Metric
	assert.Equal(t, "the-real-one", m[CanonicalWorkload], "the provider's own answer wins")
	assert.Equal(t, "something-else", m["kube_deployment"], "and the source label is left to explain it")
	// Same value under both names is not a conflict, so the rename still collapses it.
	assert.Equal(t, "api-7d9f", m[CanonicalPod])
	assert.NotContains(t, m, "pod_name")
}

func TestCanonicaliseUtilisationLabels_EmptyAndUnmappedAreSafe(t *testing.T) {
	require.NotPanics(t, func() {
		canonicaliseUtilisationLabels(nil, "datadog")
		canonicaliseUtilisationLabels([]QueryResult{{Payload: []Result{{}}}}, "datadog")
		canonicaliseUtilisationLabels([]QueryResult{{Payload: []Result{{Metric: map[string]string{"x": "y"}}}}}, "splunk_enterprise")
	})
}

// Through FetchMetricUtilisation, which is what proves the wiring rather than the
// function: a Datadog account's series reach the caller under canonical names, and
// the same request on Prometheus is unchanged. This is the cross-provider invariant
// the whole contract rests on.
func TestFetchMetricUtilisation_ReturnsCanonicalLabels(t *testing.T) {
	stubUtilisationProvider(t, "datadog", "user")
	stubMetricSource(t, &fakeMetricSource{answers: map[string][]Result{
		"cpu_usage": {{
			Metric: map[string]string{
				"pod_name":            "api-7d9f",
				"kube_container_name": "api",
				"kube_deployment":     "api",
				"unit":                "core",
			},
			Timestamps: []int64{1700000000},
			Values:     []float64{0.0366},
		}},
	}})

	end := time.Now().UTC()
	out, err := FetchMetricUtilisation(testRequestContext(), GetUtilisationTrendRequest{
		AccountId: "acc",
		StartTime: end.Add(-time.Hour).UnixMilli(),
		EndTime:   end.UnixMilli(),
		Request: map[string]any{
			"kind":               "deployment",
			"workload_namespace": "shop",
			"workload_name":      "api",
			"metrics":            []any{"cpu_usage"},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	require.Len(t, out.Results[0].Payload, 1)

	m := out.Results[0].Payload[0].Metric
	assert.Equal(t, "api-7d9f", m[CanonicalPod])
	assert.Equal(t, "api", m[CanonicalContainer])
	assert.Equal(t, "api", m[CanonicalWorkload])
	assert.NotContains(t, m, "pod_name")
	assert.Equal(t, "core", m["unit"], "unmapped labels are untouched")
	assert.Equal(t, []float64{0.0366}, out.Results[0].Payload[0].Values, "values are not the point of this pass")
}

// A CronJob-owned pod on Datadog carries both kube_cronjob and kube_job. Both map to
// `workload`, and Go randomises map iteration, so without a tie-break the same series
// gets a different workload name request to request. Run enough times that a
// coin-flip implementation cannot pass by luck.
func TestCanonicaliseUtilisationLabels_TieBreakIsDeterministic(t *testing.T) {
	for i := 0; i < 200; i++ {
		metric := map[string]string{
			"kube_cronjob":    "nightly-report",
			"kube_job":        "nightly-report-29384756",
			"kube_deployment": "nightly-report-deploy",
		}
		canonicaliseSeriesLabels(metric, "datadog", canonicalMetricLabels["datadog"])

		// controllerFacets' precedence: the outermost controller carries the clean
		// name, so kube_deployment wins over kube_job and kube_cronjob.
		require.Equal(t, "nightly-report-deploy", metric[CanonicalWorkload])
		// The losers keep their own names so the ambiguity is still visible.
		require.Equal(t, "nightly-report", metric["kube_cronjob"])
		require.Equal(t, "nightly-report-29384756", metric["kube_job"])
	}
}

// The tag really is `kube_cronjob`, per controllerFacets in
// integrations/datadog_webhook.go — ml-k8s-server's KIND_TO_TAG says `kube_cron_job`
// in two places, which is why a CronJob's Datadog queries match nothing today.
func TestCanonicaliseUtilisationLabels_CronJobUsesDatadogsRealTag(t *testing.T) {
	metric := map[string]string{"kube_cronjob": "nightly-report"}
	canonicaliseSeriesLabels(metric, "datadog", canonicalMetricLabels["datadog"])
	assert.Equal(t, "nightly-report", metric[CanonicalWorkload])
	assert.NotContains(t, metric, "kube_cronjob")

	assert.NotContains(t, canonicalMetricLabels["datadog"], "kube_cron_job",
		"kube_cron_job is not a Datadog tag; mapping it would silently do nothing")
}

// Rule 2 is enforced by canonicalLabelExempt, not merely by those labels being absent
// from the mapping tables. Drive the exempt branch directly, so deleting the check
// fails a test rather than waiting for a provider table to collide with it later.
func TestCanonicalLabelExempt_IsEnforcedNotIncidental(t *testing.T) {
	require.True(t, canonicalLabelExempt("destination_workload_name"))
	require.True(t, canonicalLabelExempt("actual_destination_workload_namespace"))
	require.False(t, canonicalLabelExempt("pod_name"))

	// A provider table that DOES name a destination label as a source: the exemption
	// is the only thing standing between it and a renamed callee.
	hostile := map[string]string{
		"destination_workload_name": CanonicalWorkload,
		"pod_name":                  CanonicalPod,
	}
	metric := map[string]string{"destination_workload_name": "checkout", "pod_name": "api-7d9f"}
	canonicaliseSeriesLabels(metric, "datadog", hostile)

	assert.Equal(t, "checkout", metric["destination_workload_name"], "callee identity survives")
	assert.NotContains(t, metric, CanonicalWorkload)
	assert.Equal(t, "api-7d9f", metric[CanonicalPod], "the queried object is still renamed")
}
