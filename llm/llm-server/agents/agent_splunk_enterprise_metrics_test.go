package agents

import (
	"nudgebee/llm/agents/core"
	"nudgebee/llm/security"
	"nudgebee/llm/services_server"
	"nudgebee/llm/tools"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Splunk Enterprise account must get the SPL-speaking agent. Falling through to
// PrometheusAgent is what made every metric value unreachable: its only execution tool
// validates PromQL, while the Splunk metric source only accepts `| mstats`.
func TestNewMetricsAgentRoutesSplunkEnterprise(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	agent := newMetricsAgent(ctx, "acct", services_server.ObservabilityProvider{Provider: "splunk_enterprise"})

	wrapper, ok := agent.(*metricsAgent)
	require.True(t, ok)
	assert.Equal(t, SplunkEnterpriseMetricsAgentName, wrapper.agent.GetName())
}

func TestSplunkEnterpriseMetricsAgent_Properties(t *testing.T) {
	agent := SplunkEnterpriseMetricsAgent{accountId: "acct"}

	assert.Equal(t, SplunkEnterpriseMetricsAgentName, agent.GetName())
	assert.NotEmpty(t, agent.GetDescription())
	assert.Equal(t, core.AgentPlannerTypeReAct, agent.GetPlannerType())
	assert.Equal(t, 10, agent.GetMaxIterations())
}

// Every tool the agent is given must speak to Splunk: the query tool is SPL-only and
// the discovery tools are pinned to the splunk_enterprise provider rather than the
// "prometheus" default they otherwise assume.
func TestSplunkEnterpriseMetricsAgent_Tools(t *testing.T) {
	agent := SplunkEnterpriseMetricsAgent{accountId: "acct"}

	var names []string
	for _, tool := range agent.GetSupportedTools(nil) {
		names = append(names, tool.Name())
		switch tl := tool.(type) {
		case tools.MetricsListTool:
			assert.Equal(t, splunkEnterpriseProvider, tl.Provider)
		case tools.ListMetricsLabelsTool:
			assert.Equal(t, splunkEnterpriseProvider, tl.Provider)
		case tools.ListMetricsLabelValuesTool:
			assert.Equal(t, splunkEnterpriseProvider, tl.Provider)
		case tools.MetricsSeriesMatchTool:
			assert.Equal(t, splunkEnterpriseProvider, tl.Provider)
		}
	}
	// metrics_series_match is the only deterministic answer to "which metrics does W
	// have"; without it the agent fell back to hand-written catalog searches.
	assert.ElementsMatch(t, []string{
		tools.ToolSplunkMetricsQuery,
		tools.ToolMetricsList,
		tools.ToolMetricsLabelsList,
		tools.ToolMetricsLabelValues,
		tools.ToolMetricsSeriesMatch,
	}, names)
	assert.NotContains(t, names, tools.ToolQueryPrometheus)
}

// The api-server rejects any metric query that does not name the configured index as a
// quoted literal, and reads values only from `nb_value`. The prompt must hand the model
// both, spelled exactly as the validator expects.
func TestSplunkEnterpriseMetricsPrompt_UsesConfiguredIndex(t *testing.T) {
	prompt := SplunkEnterpriseMetricsAgent{accountId: "acct"}.buildSystemPrompt("otel_metrics")
	text := renderPromptText(prompt)

	assert.Contains(t, text, `index="otel_metrics"`)
	assert.Contains(t, text, "AS nb_value")
	assert.Contains(t, text, "k8s.pod.cpu.usage")
	assert.Contains(t, text, "k8s.namespace.name")
	assert.Contains(t, text, tools.ToolSplunkMetricsQuery)
	assert.NotContains(t, text, "PromQL query")

	require.NotEmpty(t, prompt.Examples)
	for _, ex := range prompt.Examples {
		for _, step := range ex.AnswerSteps {
			if step.Tool == tools.ToolSplunkMetricsQuery {
				assert.Contains(t, step.Input, `index=\"otel_metrics\"`, "example must use the account's index")
				assert.Contains(t, step.Input, "AS nb_value")
			}
		}
	}
}

// `| mcatalog` through splunk_metrics_query always came back empty, and the prompt was
// what sent the agent there. Discovery goes through series match as the fallback for an
// empty value query on a known workload — the prompt must say so in that order, and must
// hand the SPL wildcard, not PromQL's =~, for a prefix group.
func TestSplunkEnterpriseMetricsPrompt_DiscoveryFallbackWithoutMcatalog(t *testing.T) {
	prompt := SplunkEnterpriseMetricsAgent{accountId: "acct"}.buildSystemPrompt("otel_metrics")
	text := renderFullPromptText(prompt)

	assert.NotContains(t, strings.ToLower(text), "mcatalog", "no part of the prompt may offer a catalog search")
	assert.NotContains(t, text, "=~", "a prefix group is filtered with an SPL wildcard, not a PromQL regex")
	assert.Contains(t, text, `<workload_label>="<workload>*"`)

	fallback := ""
	for _, line := range prompt.Instructions {
		if strings.Contains(line, "returns empty for a KNOWN workload") {
			fallback = line
		}
	}
	require.NotEmpty(t, fallback, "the empty-result fallback for a known workload must be stated")
	assert.Less(t, strings.Index(fallback, "common metric names"), strings.Index(fallback, tools.ToolMetricsSeriesMatch),
		"series match is the fallback after the value query, not step one")

	var keywordLine string
	for _, line := range prompt.Instructions {
		if strings.Contains(line, "`"+tools.ToolMetricsList+"`") && strings.Contains(line, "keyword") {
			keywordLine = line
		}
	}
	assert.Contains(t, keywordLine, "only when you do NOT have a concrete workload name",
		"keyword discovery must not compete with series match for a named workload")

	assert.Contains(t, prompt.ToolUsage, tools.ToolMetricsSeriesMatch)
	assert.Contains(t, text, "No metric series found", "a complete empty series match must end the search")
}

// With no metrics index configured the api-server fails every metric query, so the
// agent must say metrics are not configured instead of guessing an index.
func TestSplunkEnterpriseMetricsPrompt_NoIndex(t *testing.T) {
	text := renderPromptText(SplunkEnterpriseMetricsAgent{accountId: "acct"}.buildSystemPrompt(""))

	assert.Contains(t, text, "no metrics index")
	assert.NotContains(t, text, `index=""`)
}

func renderPromptText(p core.NBAgentPrompt) string {
	var b strings.Builder
	b.WriteString(p.Role)
	for _, s := range p.Instructions {
		b.WriteString("\n" + s)
	}
	for _, s := range p.Constraints {
		b.WriteString("\n" + s)
	}
	for tool, lines := range p.ToolUsage {
		b.WriteString("\n" + tool)
		for _, l := range lines {
			b.WriteString("\n" + l)
		}
	}
	for _, ex := range p.Examples {
		for _, st := range ex.AnswerSteps {
			b.WriteString("\n" + st.Input)
		}
	}
	return b.String()
}

// renderFullPromptText renders every field the model sees, examples and output format
// included, so a negative assertion cannot pass just because the text hid somewhere
// renderPromptText skips.
func renderFullPromptText(p core.NBAgentPrompt) string {
	var b strings.Builder
	b.WriteString(renderPromptText(p))
	for _, ex := range p.Examples {
		b.WriteString("\n" + ex.Question + "\n" + ex.Explanation)
		for _, st := range ex.AnswerSteps {
			b.WriteString("\n" + st.Tool + "\n" + st.Explanation)
		}
	}
	b.WriteString("\n" + p.OutputFormat)
	return b.String()
}
