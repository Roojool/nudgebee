package tools

import (
	"testing"

	"nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
)

func TestMetricsSeriesMatchTool_Metadata(t *testing.T) {
	tool := MetricsSeriesMatchTool{Provider: "prometheus"}
	assert.Equal(t, ToolMetricsSeriesMatch, tool.Name())
	assert.Equal(t, core.NBToolTypeTool, tool.GetType())
	schema := tool.InputSchema()
	assert.Contains(t, schema.Properties, "workload")
	assert.Contains(t, schema.Properties, "namespace")
	assert.Equal(t, []string{"workload"}, schema.Required)
}

func TestMetricsSeriesMatchTool_RequiresWorkload(t *testing.T) {
	tool := MetricsSeriesMatchTool{Provider: "prometheus"}
	// Completely empty request (no command, no arguments) → tool validates required 'workload' field.
	// Note: when called via callNbTool, validateToolInput fires first with a schema-aware message.
	// Direct Call() goes straight to the tool's own validation.
	resp, err := tool.Call(core.NbToolContext{}, core.NBToolCallRequest{})
	assert.NoError(t, err)
	assert.Equal(t, core.NBToolResponseStatusError, resp.Status)
	assert.Contains(t, resp.Data, "workload")
}

func TestMetricsSeriesMatchTool_RequiresAccountContext(t *testing.T) {
	tool := MetricsSeriesMatchTool{Provider: "prometheus"}
	// Workload present but no request context → fail fast (would otherwise panic on logging).
	resp, err := tool.Call(core.NbToolContext{}, core.NBToolCallRequest{Arguments: map[string]any{"workload": "llm-server"}})
	assert.Error(t, err)
	assert.Equal(t, core.NBToolResponseStatusError, resp.Status)
}

func TestArgString(t *testing.T) {
	in := core.NBToolCallRequest{Arguments: map[string]any{
		"workload":  "llm-server",
		"namespace": "nudgebee",
		"num":       42,
	}}
	assert.Equal(t, "llm-server", argString(in, "workload"))
	assert.Equal(t, "nudgebee", argString(in, "namespace"))
	assert.Equal(t, "42", argString(in, "num"))
	assert.Equal(t, "", argString(in, "missing"))
	assert.Equal(t, "", argString(core.NBToolCallRequest{}, "workload"))
}

func TestSeriesMatchCacheTTL_Default(t *testing.T) {
	// With no override configured, the default is 30m (series change slowly).
	assert.Equal(t, 30*60, int(seriesMatchCacheTTL().Seconds()))
}

// A lookup that was cut short and a lookup that finished empty-handed are different
// findings. Collapsing them is how a large metric catalog gets reported as no metrics.
func TestSeriesMatchEmptyResponse_TruncatedIsInconclusive(t *testing.T) {
	complete := seriesMatchEmptyResponse("checkout", "prod", false)
	assert.Equal(t, core.NBToolResponseStatusSuccess, complete.Status)
	assert.Contains(t, complete.Data, "No metric series found")
	assert.NotContains(t, complete.Data, "Inconclusive")

	partial := seriesMatchEmptyResponse("checkout", "prod", true)
	assert.Equal(t, core.NBToolResponseStatusSuccess, partial.Status)
	assert.Contains(t, partial.Data, "Inconclusive")
	assert.Contains(t, partial.Data, "Do NOT conclude the workload has no metrics")
}
