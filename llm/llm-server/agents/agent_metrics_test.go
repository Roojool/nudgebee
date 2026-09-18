package agents

import (
	"nudgebee/llm/security"
	"nudgebee/llm/services_server"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildLargeMetricsMessage asserts the metrics wrapper's truncation notice tells
// the calling orchestrator to grep the saved file instead of re-querying with a
// narrower time range (NB-27743), and applies uniformly regardless of provider — the
// saved file holds the sub-agent's own answer text either way (PostProcessResponse's
// raw-JSON override never fires through this nested ExecuteAgentToolCall path), so
// the guidance is plain-text search, not a provider-specific data shape.
func TestBuildLargeMetricsMessage(t *testing.T) {
	msg := buildLargeMetricsMessage(12345, "metrics_123.txt", "some preview text")

	assert.Contains(t, msg, "Output large (12345 bytes)")
	assert.Contains(t, msg, "metrics_123.txt")
	assert.Contains(t, msg, "shell_execute")
	assert.Contains(t, msg, "instead of re-querying with a narrower time range")
	assert.Contains(t, msg, "some preview text")

	// Must not assume a specific data shape (JSON/jq) — the saved content is the
	// sub-agent's own prose answer, not raw per-datapoint data.
	assert.NotContains(t, msg, "jq")
	assert.NotContains(t, msg, "map_values(fromjson)")
}

// Each query writer predates the metrics_query wrapper and answers in its own shape.
// These cases are the contract the wrapper has to keep honouring; a writer changing
// its output shape should fail here rather than silently deliver an empty editor.
func TestNormaliseGeneratedMetricsQuery(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		raw          string
		wantQuery    string
		wantProvider string
		wantIndex    string
	}{
		{
			name:         "promql_query single query",
			provider:     "prometheus",
			raw:          `{"promql": "sum(rate(container_cpu_usage_seconds_total{namespace=\"nudgebee\"}[5m])) by (pod)"}`,
			wantQuery:    `sum(rate(container_cpu_usage_seconds_total{namespace="nudgebee"}[5m])) by (pod)`,
			wantProvider: "prometheus",
		},
		{
			// promql_query returns an array when asked for several; the editor holds one.
			name:         "promql_query array takes the first",
			provider:     "victoria-metrics",
			raw:          `{"promql": ["up{job=\"a\"}", "up{job=\"b\"}"]}`,
			wantQuery:    `up{job="a"}`,
			wantProvider: "victoria-metrics",
		},
		{
			name:         "promql_query array skips a leading empty entry",
			provider:     "prometheus",
			raw:          `{"promql": ["", "up"]}`,
			wantQuery:    "up",
			wantProvider: "prometheus",
		},
		{
			name:         "es envelope passes through with its index",
			provider:     "ES",
			raw:          `{"query":"{\n  \"query\": {}\n}","provider":"ES","index":"metrics-kubernetes.*"}`,
			wantQuery:    "{\n  \"query\": {}\n}",
			wantProvider: "ES",
			wantIndex:    "metrics-kubernetes.*",
		},
		{
			name:         "datadog bare query string",
			provider:     "datadog",
			raw:          "avg:kubernetes.cpu.usage.total{kube_namespace:nudgebee}",
			wantQuery:    "avg:kubernetes.cpu.usage.total{kube_namespace:nudgebee}",
			wantProvider: "datadog",
		},
		{
			name:         "fenced bare query is unwrapped",
			provider:     "datadog",
			raw:          "```\navg:system.cpu.user{*}\n```",
			wantQuery:    "avg:system.cpu.user{*}",
			wantProvider: "datadog",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normaliseGeneratedMetricsQuery(tc.provider, tc.raw)

			require.NoError(t, err)
			assert.Equal(t, tc.wantQuery, got.Query)
			assert.Equal(t, tc.wantProvider, got.Provider)
			assert.Equal(t, tc.wantIndex, got.Index)
		})
	}
}

// An empty query box after a spinner reads as "the AI is broken" and gives the user
// nothing to act on, so every unusable answer must surface as an error instead.
func TestNormaliseGeneratedMetricsQueryRejectsUnusableAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "empty response", raw: ""},
		{name: "whitespace only", raw: "   \n  "},
		{name: "json object with neither query nor promql", raw: `{"summary":"cpu looks fine"}`},
		{name: "empty promql string", raw: `{"promql": ""}`},
		{name: "promql array with only empty entries", raw: `{"promql": ["", "  "]}`},
		{name: "empty es query field", raw: `{"query":"","provider":"ES"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normaliseGeneratedMetricsQuery("prometheus", tc.raw)
			require.Error(t, err)
		})
	}
}

// The router picks the query writer whose language the account's editor speaks. It
// also proves the registry lookups resolve: promql_query and datadog_metrics_query
// register via RegisterNBAgentFactoryAsTool, and a rename there would otherwise only
// surface as a runtime "not registered" error on a user's click.
func TestNewMetricsQueryAgentSelectsWriterByProvider(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	for _, tc := range []struct {
		provider   string
		wantWriter string
	}{
		{provider: "prometheus", wantWriter: PromqlAgentName},
		{provider: "victoria-metrics", wantWriter: PromqlAgentName},
		{provider: "chronosphere", wantWriter: PromqlAgentName},
		{provider: "", wantWriter: PromqlAgentName},
		{provider: "ES", wantWriter: ESMetricsQueryAgentName},
		{provider: "elasticsearch", wantWriter: ESMetricsQueryAgentName},
		{provider: "opensearch", wantWriter: ESMetricsQueryAgentName},
		{provider: "datadog", wantWriter: DatadogMetricsQueryAgentName},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			agent, err := newMetricsQueryAgent(ctx, "acct", "", services_server.ObservabilityProvider{Provider: tc.provider})

			require.NoError(t, err)
			wrapper, ok := agent.(*metricsQueryAgent)
			require.True(t, ok)
			assert.Equal(t, tc.wantWriter, wrapper.agent.GetName())
			assert.Equal(t, tc.provider, wrapper.provider, "provider is echoed back in the envelope")
		})
	}
}

// Cloud-only accounts read metrics through a CLI agent, so there is no query language
// for the editor. Generating one anyway would hand the user a string Run Query can
// never execute.
func TestNewMetricsQueryAgentRejectsCloudOnlyProviders(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	for _, provider := range []string{"aws", "gcp", "azure"} {
		t.Run(provider, func(t *testing.T) {
			_, err := newMetricsQueryAgent(ctx, "acct", "", services_server.ObservabilityProvider{Provider: provider})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "not supported")
		})
	}
}

// The ES writer resolves its schema against the pinned index, so the pin has to reach
// it rather than being dropped at the router.
func TestNewMetricsQueryAgentPinsESIndex(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	agent, err := newMetricsQueryAgent(ctx, "acct", "metrics-kubernetes.*", services_server.ObservabilityProvider{Provider: "ES"})

	require.NoError(t, err)
	wrapper := agent.(*metricsQueryAgent)
	esWriter, ok := wrapper.agent.(*ESMetricsQueryAgent)
	require.True(t, ok)
	assert.Equal(t, "metrics-kubernetes.*", esWriter.requestedIndex)
}
