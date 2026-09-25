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
		}
	}
	assert.ElementsMatch(t, []string{
		tools.ToolSplunkMetricsQuery,
		tools.ToolMetricsList,
		tools.ToolMetricsLabelsList,
		tools.ToolMetricsLabelValues,
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
