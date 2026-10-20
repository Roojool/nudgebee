package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"nudgebee/llm/config"
	"nudgebee/llm/tools/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// startSeriesMatchStub stands in for api-server's metrics_list_series RPC and points
// config.Config.ServiceEndpoint at it. Every request's decoded input.request is appended
// to *requests, so a test can count round-trips and read the provider that was sent.
func startSeriesMatchStub(t *testing.T, body string, requests *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rpc/metrics", r.URL.Path)
		var payload struct {
			Input struct {
				Request map[string]any `json:"request"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		*requests = append(*requests, payload.Input.Request)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))

	prev := config.Config.ServiceEndpoint
	config.Config.ServiceEndpoint = srv.URL
	t.Cleanup(func() {
		config.Config.ServiceEndpoint = prev
		srv.Close()
	})
}

func seriesMatchCall(t *testing.T, tool MetricsSeriesMatchTool, workload string) core.NBToolResponse {
	t.Helper()
	resp, err := tool.Call(newTriageToolContext("acc-series"), core.NBToolCallRequest{
		Arguments: map[string]any{"workload": workload, "namespace": "demo"},
	})
	require.NoError(t, err)
	require.Equal(t, core.NBToolResponseStatusSuccess, resp.Status, resp.Data)
	return resp
}

// A prefix group is only reachable through a wildcard, and PromQL and SPL spell it
// differently. The Splunk agent was being told to write =~, which its SPL tool cannot run.
func TestMetricsSeriesMatchTool_PrefixHintFollowsProvider(t *testing.T) {
	prom := MetricsSeriesMatchTool{Provider: "prometheus"}.Description()
	assert.Contains(t, prom, `use =~"<workload>.*")`, "the PromQL hint must be unchanged for Prometheus-shaped providers")
	assert.NotContains(t, prom, "k8s.pod.name")

	splunk := MetricsSeriesMatchTool{Provider: "splunk_enterprise"}.Description()
	assert.Contains(t, splunk, `<workload_label>="<workload>*"`)
	assert.NotContains(t, splunk, "=~")
}

func TestMetricsSeriesMatchTool_CallNoteFollowsProvider(t *testing.T) {
	const body = `{"matches":[{"namespace_label":"k8s.namespace.name","workload_label":"k8s.pod.name","match_type":"prefix","families":["k8s.pod.cpu.usage"]}],"truncated":false}`

	t.Run("splunk gets the SPL wildcard", func(t *testing.T) {
		var requests []map[string]any
		startSeriesMatchStub(t, body, &requests)

		resp := seriesMatchCall(t, MetricsSeriesMatchTool{Provider: "splunk_enterprise"}, "note-splunk-"+uuid.NewString())

		require.Len(t, requests, 1)
		assert.Equal(t, "splunk_enterprise", requests[0]["metric_provider"])
		var out struct {
			Note string `json:"note"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.Data), &out))
		assert.Contains(t, out.Note, `k8s.pod.name="<workload>*"`)
		assert.NotContains(t, out.Note, "=~")
	})

	t.Run("prometheus note is unchanged", func(t *testing.T) {
		var requests []map[string]any
		startSeriesMatchStub(t, body, &requests)

		resp := seriesMatchCall(t, MetricsSeriesMatchTool{Provider: "prometheus"}, "note-prom-"+uuid.NewString())

		var out struct {
			Note string `json:"note"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.Data), &out))
		assert.Equal(t, `Pick a family from a group's 'families' and filter it by THAT group's namespace_label and workload_label. match_type "prefix" => use =~"<workload>.*" (e.g. pod). Do not mix labels across groups.`, out.Note)
	})
}

// "No series" from a sweep that searched every convention is a settled answer; re-asking
// re-ran the whole sweep for it. A cut-short sweep is not an answer, so it must not be
// cached — the next call has to be free to finish the search.
func TestMetricsSeriesMatchTool_CachesOnlyCompleteEmptyResults(t *testing.T) {
	t.Run("complete empty is cached", func(t *testing.T) {
		var requests []map[string]any
		startSeriesMatchStub(t, `{"matches":[],"truncated":false}`, &requests)
		tool := MetricsSeriesMatchTool{Provider: "splunk_enterprise"}
		workload := "empty-complete-" + uuid.NewString()

		first := seriesMatchCall(t, tool, workload)
		second := seriesMatchCall(t, tool, workload)

		assert.Contains(t, first.Data, "No metric series found")
		assert.Equal(t, first, second)
		assert.Len(t, requests, 1, "the second call must be served from the cache")
	})

	t.Run("truncated empty is not cached", func(t *testing.T) {
		var requests []map[string]any
		startSeriesMatchStub(t, `{"matches":[],"truncated":true}`, &requests)
		tool := MetricsSeriesMatchTool{Provider: "splunk_enterprise"}
		workload := "empty-truncated-" + uuid.NewString()

		first := seriesMatchCall(t, tool, workload)
		seriesMatchCall(t, tool, workload)

		assert.Contains(t, first.Data, "Inconclusive")
		assert.Len(t, requests, 2, "an inconclusive lookup must be retried, not replayed from the cache")
	})
}
