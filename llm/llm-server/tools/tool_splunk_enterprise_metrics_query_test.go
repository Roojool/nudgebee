package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The api-server Splunk metric source only accepts SPL that opens with a metrics
// generating command, and only reads the value out of the `nb_value` column. Catching
// both here turns a silent "no numeric value column" into an error the agent can fix.
func TestValidateSplunkMetricsSPL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spl     string
		wantErr string
	}{
		{
			name: "mstats with nb_value alias",
			spl:  `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics" AND k8s.namespace.name="nudgebee" span=1m BY k8s.pod.name`,
		},
		{
			name: "alias is case-insensitive",
			spl:  `| mstats max(k8s.pod.memory.usage) as nb_value WHERE index="otel_metrics" span=5m`,
		},
		{
			name:    "empty",
			spl:     "   ",
			wantErr: "empty",
		},
		{
			name:    "promql is rejected with an SPL hint",
			spl:     `rate(container_cpu_usage_seconds_total{namespace="nudgebee"}[5m])`,
			wantErr: "| mstats",
		},
		{
			name:    "mstats without leading pipe",
			spl:     `mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics"`,
			wantErr: "must start with",
		},
		{
			name:    "event search is not a metrics query",
			spl:     `| search index="otel_metrics"`,
			wantErr: "| mstats",
		},
		{
			name:    "mstats without nb_value alias",
			spl:     `| mstats avg(k8s.pod.cpu.usage) WHERE index="otel_metrics" span=1m`,
			wantErr: "AS nb_value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSplunkMetricsSPL(tc.spl)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// A catalog search always came back empty through this tool — the metric decoder drops
// its one multivalue row — and the agent kept rewriting it instead of moving on. The
// rejection has to name every tool that answers the question it was asking.
func TestValidateSplunkMetricsSPL_RejectsMcatalogWithDiscoveryTools(t *testing.T) {
	for _, spl := range []string{
		`| mcatalog values(metric_name) WHERE index="otel_metrics"`,
		`| MCATALOG values(k8s.pod.name) WHERE index="otel_metrics" AND metric_name="k8s.pod.cpu.usage"`,
	} {
		err := validateSplunkMetricsSPL(spl)
		require.Error(t, err, spl)
		for _, tool := range []string{ToolMetricsSeriesMatch, ToolMetricsList, ToolMetricsLabelsList, ToolMetricsLabelValues} {
			assert.Contains(t, err.Error(), tool, "the rejection must point at %s", tool)
		}
	}
}

// The description is what the planner reads before choosing a tool; offering mcatalog
// there is how the dead end was reached in the first place.
func TestSplunkMetricsQueryTool_DescriptionOffersNoCatalogSearch(t *testing.T) {
	desc := SplunkMetricsQueryTool{}.Description()
	assert.NotContains(t, desc, "mcatalog")
	assert.Contains(t, desc, "| mstats")
	assert.Contains(t, desc, ToolMetricsSeriesMatch)
}

// Models sometimes nest the whole tool input inside `command` or wrap the SPL in
// backticks; both must reach validation as bare SPL.
func TestNormaliseSplunkMetricsCommand(t *testing.T) {
	const spl = `| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index="otel_metrics"`

	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "bare", input: spl},
		{name: "backticks", input: "`" + spl + "`"},
		{name: "code fence", input: "```spl\n" + spl + "\n```"},
		{name: "nested command json", input: `{"command": "| mstats avg(k8s.pod.cpu.usage) AS nb_value WHERE index=\"otel_metrics\"", "range": "15m"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, spl, normaliseSplunkMetricsCommand(tc.input))
		})
	}
}
