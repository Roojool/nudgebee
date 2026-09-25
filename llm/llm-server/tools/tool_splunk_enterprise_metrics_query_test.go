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
			name: "mcatalog needs no alias",
			spl:  `| mcatalog values(metric_name) WHERE index="otel_metrics"`,
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
