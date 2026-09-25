package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/llm/config"
	"nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startMetricsLabelsStub serves /rpc/metrics metrics_list_labels, answering with fields
// for the indices in `known` and a 400 for anything else. It records the index asked
// for on each call.
func startMetricsLabelsStub(t *testing.T, known map[string]string) (asked *[]string, cleanup func()) {
	t.Helper()
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input struct {
				Request struct {
					Metric string `json:"metric"`
				} `json:"request"`
			} `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		index := payload.Input.Request.Metric
		requested = append(requested, index)

		w.Header().Set("Content-Type", "application/json")
		field, ok := known[index]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"no fields found for index \"` + index + `\""}`))
			return
		}
		_, _ = w.Write([]byte(`[{"label":"` + field + `","attributes":{"type":"keyword"}}]`))
	}))

	prev := config.Config.ServiceEndpoint
	config.Config.ServiceEndpoint = srv.URL
	return &requested, func() {
		config.Config.ServiceEndpoint = prev
		srv.Close()
	}
}

// An exact index name, including a remote-cluster one, must be listed as asked. It used
// to be swapped for the configured index before the call because it had no wildcard, so
// the model received the configured index's fields as if they were the requested one's.
func TestListMetricsLabelsTool_ES_UsesRequestedIndex(t *testing.T) {
	asked, cleanup := startMetricsLabelsStub(t, map[string]string{
		"remote:metrics-aws.cloudwatch_metrics-x": "metricset.dimensions.DBClusterIdentifier",
		"metrics-kubernetes.*":                    "kubernetes.namespace",
	})
	defer cleanup()

	tool := ListMetricsLabelsTool{Provider: "ES", DefaultIndex: "metrics-kubernetes.*"}
	resp, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{Command: "remote:metrics-aws.cloudwatch_metrics-x"})
	require.NoError(t, err)

	assert.Equal(t, []string{"remote:metrics-aws.cloudwatch_metrics-x"}, *asked, "no swap, no second call")
	assert.Contains(t, resp.Data, "metricset.dimensions.DBClusterIdentifier")
	assert.NotContains(t, resp.Data, "kubernetes.namespace")
	assert.NotContains(t, resp.Data, "note")
}

// A metric name passed where an index belongs still resolves, through the configured
// index — and the response says the fields are the configured index's, not the input's.
func TestListMetricsLabelsTool_ES_FallsBackWithNote(t *testing.T) {
	asked, cleanup := startMetricsLabelsStub(t, map[string]string{
		"metrics-kubernetes.*": "kubernetes.namespace",
	})
	defer cleanup()

	tool := ListMetricsLabelsTool{Provider: "ES", DefaultIndex: "metrics-kubernetes.*"}
	resp, err := tool.Call(newTriageToolContext("acc-1"), core.NBToolCallRequest{Command: "kubernetes.pod.cpu.usage"})
	require.NoError(t, err)
	assert.Equal(t, core.NBToolResponseStatusSuccess, resp.Status)

	assert.Equal(t, []string{"kubernetes.pod.cpu.usage", "metrics-kubernetes.*"}, *asked)
	var out struct {
		Note   string           `json:"note"`
		Labels []map[string]any `json:"labels"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Data), &out))
	assert.Contains(t, out.Note, `"metrics-kubernetes.*"`, "the note names the index the fields really came from")
	require.Len(t, out.Labels, 1)
	assert.Equal(t, "kubernetes.namespace", out.Labels[0]["label"])
}
