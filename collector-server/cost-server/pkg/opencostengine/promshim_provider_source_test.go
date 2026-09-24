package opencostengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The shim must NOT pin metric_provider_source.
//
// Naming both the provider and the source makes api-server skip provider resolution
// altogether and send every query down the agent relay. An account whose Prometheus is
// connected over HTTP, with no agent in the cluster, then gets "agent not connected"
// for every allocation query — and because the shim fails closed rather than returning
// empty series, cost-server reports nothing at all rather than a wrong number.
//
// Leaving the source empty lets api-server consult the account's own Prometheus
// integration, which still resolves to the agent for accounts that have one.
func TestShimDoesNotPinTheProviderSource(t *testing.T) {
	var captured map[string]any
	// The handler records rather than asserts: t.Fatalf calls runtime.Goexit, which
	// must happen on the test's own goroutine. Called from here it would abandon the
	// HTTP handler mid-request and leave the test hanging on a response that never
	// comes, reported as a timeout rather than as the failure it is.
	var handlerErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			handlerErr = fmt.Errorf("read body: %w", err)
		} else if err := json.Unmarshal(body, &captured); err != nil {
			handlerErr = fmt.Errorf("decode body: %w", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"query_key":"q","payload":[]}]}`))
	}))
	defer server.Close()

	shim := &PrometheusShim{
		serviceURL:   server.URL,
		serviceToken: "token",
		httpClient:   server.Client(),
	}

	if _, err := shim.fetchMetric(context.Background(), "acct-1", `up`, 1000, 2000, true); err != nil {
		t.Fatalf("fetchMetric: %v", err)
	}
	if handlerErr != nil {
		t.Fatalf("handler: %v", handlerErr)
	}

	input, ok := captured["input"].(map[string]any)
	if !ok {
		t.Fatalf("no input in %v", captured)
	}
	request, ok := input["request"].(map[string]any)
	if !ok {
		t.Fatalf("no request in %v", input)
	}

	if _, pinned := request["metric_provider_source"]; pinned {
		t.Errorf("metric_provider_source must be absent so the account's own integration resolves; got %v",
			request["metric_provider_source"])
	}

	// The PROVIDER stays pinned: these are OpenCost's own PromQL strings, and handing
	// them to Datadog or Elasticsearch would not parse.
	if got := request["metric_provider"]; got != "prometheus" {
		t.Errorf("metric_provider = %v, want prometheus", got)
	}
}
