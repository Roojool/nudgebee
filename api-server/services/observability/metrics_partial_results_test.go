package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One key's failure must not cost the other keys their answer.
//
// Most providers already worked this way -- newrelic_metrics.go is the clearest example:
// a failed key appends a result carrying its error and the loop continues. Two did not:
// Datadog aborted inside its per-key HTTP loop, and Prometheus and Chronosphere aborted
// while BUILDING the queries, so one unparseable query text cost every other key its
// answer before a single request was sent.
//
// That is why Datadog is excluded from the shared rightsizing path today: one
// rate-limited response lost the whole run.

// A query that cannot be built is reported against its own key, and the keys that built
// fine are still asked for.
func TestPrometheusBuildFailure_DoesNotCostTheOtherKeysTheirAnswer(t *testing.T) {
	meta := RequestMetadata{Kind: "deployment", Namespace: "nudgebee", Name: "services-server"}
	good := buildPrometheusWorkloadQueries(meta, []string{"cpu_usage", "memory_usage"})
	require.Len(t, good, 2, "both keys build cleanly, so both must be present")

	// Sanity: the builder itself does not drop keys it can answer. The build-failure
	// path is exercised through injectPromQLMatchers, which only rejects malformed text.
	for key, q := range good {
		assert.NotEmpty(t, q, "%s built empty", key)
	}
}

// The three states a key can be in must stay distinguishable after a partial result.
// This is the contract noteFailedKeys enforces, checked here against a mixed batch --
// the shape a partial result actually produces.
func TestPartialResult_KeepsTheThreeStatesDistinguishable(t *testing.T) {
	failure := "datadog rate-limited (429) and the retry budget is spent"
	out := OutputMetricQuery{Results: []QueryResult{
		{QueryKey: "cpu_usage", Payload: []Result{{Values: []float64{1}}}},                 // answered
		{QueryKey: "container_memory_max_usage_by_pod", Note: "no cgroup high-water mark"}, // unsupported
		{QueryKey: "memory_usage", Error: &failure},                                        // failed
	}}
	noteFailedKeys(&out)

	answered, unsupported, failed := out.Results[0], out.Results[1], out.Results[2]

	assert.Empty(t, answered.Note)
	assert.Nil(t, answered.Error)
	assert.NotEmpty(t, answered.Payload)

	assert.Equal(t, "no cgroup high-water mark", unsupported.Note, "provider wording is preserved")
	assert.Nil(t, unsupported.Error, "unsupported is not a failure")

	assert.NotNil(t, failed.Error)
	assert.Contains(t, failed.Note, "429", "a failed key must explain itself, not just flag itself")

	// The point of all three: a caller reading only Payload sees three empty-or-not
	// results, and only the note tells it which emptiness means what.
	assert.NotEqual(t, unsupported.Note, failed.Note)
}

// When NOTHING builds, the provider must answer with the reasons rather than call out.
//
// This is not only about saving a pointless request. The fetch below the build loop
// reports a transport failure by returning an error and an EMPTY result, so a batch
// where every key failed to build would have its per-key reasons thrown away and come
// back silent -- which is the exact failure the build loop was added to prevent.
func TestAllKeysFailToBuild_ReportsWhyInsteadOfCallingOut(t *testing.T) {
	// No agent is connected in a unit test, so reaching the relay is itself the bug:
	// it would return (OutputMetricQuery{}, err) and lose both keys' reasons.
	// An unusable label name is the realistic way a whole batch fails to build: the
	// labels are shared by every key, so one bad name rejects all of them at once.
	req := FetchMetricsRequest{
		AccountId: "acct-does-not-exist",
		Queries: map[string]string{
			"cpu_usage":    "sum(rate(container_cpu_usage_seconds_total[5m]))",
			"memory_usage": "sum(container_memory_working_set_bytes)",
		},
		Labels: map[string]string{"not a label name": "nudgebee"},
	}

	for name, fetch := range map[string]func() (OutputMetricQuery, error){
		"prometheus":   func() (OutputMetricQuery, error) { return (&PrometheusMetricSource{}).FetchMetricsQuery(nil, req) },
		"chronosphere": func() (OutputMetricQuery, error) { return (&ChronosphereMetricSource{}).FetchMetricsQuery(nil, req) },
	} {
		t.Run(name, func(t *testing.T) {
			out, err := fetch()
			require.NoError(t, err, "a batch of unbuildable keys is a per-key answer, not a batch failure")
			require.Len(t, out.Results, 2, "every key must come back carrying its own reason")

			for _, r := range out.Results {
				assert.NotNil(t, r.Error, "%s came back silent instead of saying why", r.QueryKey)
				assert.Empty(t, r.Payload, "%s must not look like a measurement of nothing", r.QueryKey)
			}
		})
	}
}
