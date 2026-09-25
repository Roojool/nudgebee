package observability

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workload_http_request_count / workload_http_error_count exist so a consumer can apply
// its own smoothing to the error ratio. Two properties have to hold or that consumer's
// tuning is silently wrong:
//
//  1. the window is the REQUESTED step, not a hardcoded one — a constant fitted against
//     2m buckets is meaningless if the layer decides the bucket itself;
//  2. the workload is matched exactly on destination_workload_name, the label that
//     carries workload names. The actual_destination_* labels carry POD names and need a
//     prefix, which also matches a sibling workload ("services-server" catches
//     "services-server-worker").

func httpCountMeta(step string) RequestMetadata {
	return RequestMetadata{Kind: "deployment", Namespace: "nudgebee", Name: "services-server", Step: step}
}

func TestWorkloadHTTPCountKeys_UseTheRequestedStepAsTheWindow(t *testing.T) {
	for _, step := range []string{"2m", "30s", "1h"} {
		queries := buildPrometheusWorkloadQueries(httpCountMeta(step),
			[]string{"workload_http_request_count", "workload_http_error_count"})
		require.Len(t, queries, 2, "step %s", step)
		for key, q := range queries {
			assert.Contains(t, q, "["+step+"]", "%s should window over the requested step: %s", key, q)
		}
	}
}

// No step supplied must still produce a usable query rather than an empty window.
func TestWorkloadHTTPCountKeys_DefaultStepWhenUnset(t *testing.T) {
	queries := buildPrometheusWorkloadQueries(httpCountMeta(""),
		[]string{"workload_http_request_count", "workload_http_error_count"})
	require.Len(t, queries, 2)
	for key, q := range queries {
		assert.Contains(t, q, "[5m]", "%s: %s", key, q)
		assert.NotContains(t, q, "[]", "%s has an empty window: %s", key, q)
	}
}

func TestWorkloadHTTPCountKeys_MatchTheWorkloadExactly(t *testing.T) {
	queries := buildPrometheusWorkloadQueries(httpCountMeta("2m"),
		[]string{"workload_http_request_count", "workload_http_error_count"})
	for key, q := range queries {
		assert.Contains(t, q, `destination_workload_name="services-server"`, "%s: %s", key, q)
		assert.NotContains(t, q, "services-server.*", "%s prefix-matches: %s", key, q)
		assert.NotContains(t, q, "actual_destination_workload_name", "%s uses the pod-name label: %s", key, q)
	}
}

// The only difference between the two keys is the status selector: the error count must
// narrow to 4xx/5xx and the request count must not, or the ratio is 1 everywhere.
func TestWorkloadHTTPCountKeys_OnlyTheErrorKeyFiltersOnStatus(t *testing.T) {
	queries := buildPrometheusWorkloadQueries(httpCountMeta("2m"),
		[]string{"workload_http_request_count", "workload_http_error_count"})
	assert.NotContains(t, queries["workload_http_request_count"], "status")
	assert.Contains(t, queries["workload_http_error_count"], `status=~"^[45][0-9][0-9]$"`)

	stripped := strings.Replace(queries["workload_http_error_count"], `, status=~"^[45][0-9][0-9]$"`, "", 1)
	assert.Equal(t, queries["workload_http_request_count"], stripped,
		"the two keys should differ only by the status selector")
}

// Datadog answers the same two keys from APM traces. It must answer them rather than
// stay silent: a key missing from the response is indistinguishable from a workload that
// served no traffic, and the epic's contract is empty-with-a-reason, never silence.
func TestWorkloadHTTPCountKeys_DatadogAnswersFromAPMTraces(t *testing.T) {
	meta := RequestMetadata{Kind: "deployment", Namespace: "nudgebee", Name: "services-server", Step: "120s"}
	queries := buildDatadogWorkloadQueries(meta, []string{"workload_http_request_count", "workload_http_error_count"})
	require.Len(t, queries, 2, "Datadog must answer both count keys, not drop them")

	assert.Contains(t, queries["workload_http_request_count"], "trace.http.server.request.hits")
	assert.Contains(t, queries["workload_http_error_count"], "trace.http.server.request.errors")
	for key, q := range queries {
		assert.Contains(t, q, "service:services-server", "%s: %s", key, q)
		// Counts, not rates, and bucketed by the requested step -- a consumer
		// calibrating a constant against these depends on the bucket width.
		assert.Contains(t, q, ".as_count()", "%s: %s", key, q)
		assert.Contains(t, q, "rollup(sum, 120)", "%s should bucket by the requested step: %s", key, q)
	}
}

// Without a workload name there is no APM service to scope to, so Datadog answers
// neither key rather than returning every service's traffic.
func TestWorkloadHTTPCountKeys_DatadogNeedsAWorkloadName(t *testing.T) {
	meta := RequestMetadata{Kind: "deployment", Namespace: "nudgebee", Step: "120s"}
	queries := buildDatadogWorkloadQueries(meta, []string{"workload_http_request_count", "workload_http_error_count"})
	assert.Empty(t, queries)
}
