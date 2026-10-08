package observability

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestVerifyElasticAPMReader_AgainstLiveCluster is an offline verification harness,
// NOT a CI unit test. The unit tests in elasticsearch_apm_traces_test.go pin the
// mapper against documents transcribed by hand; this one proves the SAME code against
// whatever an actual Elastic APM cluster returns, which is the only way to catch a
// field path that does not exist in a real mapping.
//
// It runs the reader's own DSL builder and its own hit mapper, so a field path typo
// shows up as an empty canonical field rather than as a passing assertion.
//
// Skipped unless ES_APM_VERIFY_URL is set, so `make test` / CI never reach a cluster:
//
//	kubectl --context <ctx> -n elasticsearch port-forward svc/elasticsearch-es-http 9200:9200
//	ES_APM_VERIFY_URL='https://localhost:9200' \
//	ES_APM_VERIFY_USER='elastic' \
//	ES_APM_VERIFY_PASS="$(kubectl get secret -n elasticsearch elasticsearch-es-elastic-user -o go-template='{{.data.elastic | base64decode}}')" \
//	go test ./observability/ -run TestVerifyElasticAPMReader_AgainstLiveCluster -v
//
// ES_APM_VERIFY_INDEX overrides the index pattern (default traces-apm*).
func TestVerifyElasticAPMReader_AgainstLiveCluster(t *testing.T) {
	baseURL := strings.TrimRight(os.Getenv("ES_APM_VERIFY_URL"), "/")
	if baseURL == "" {
		t.Skip("ES_APM_VERIFY_URL not set — skipping live Elastic APM verification harness")
	}
	index := os.Getenv("ES_APM_VERIFY_INDEX")
	if index == "" {
		index = esAPMTraceIndex
	}

	// The reader's real query builder, with a wide window so the harness does not
	// depend on traffic in the last few minutes.
	req := TracesV3Request{
		StartTime:    0,
		EndTime:      0,
		QueryRequest: TracesQueryBuilderRequest{Limit: 200},
	}
	dsl := buildAPMTraceQuery(req, elasticAPMTraceLabelMapping)

	body, err := json.Marshal(dsl)
	if err != nil {
		t.Fatalf("marshal dsl: %v", err)
	}
	t.Logf("DSL:\n%s", string(body))

	httpReq, err := http.NewRequest("POST", fmt.Sprintf("%s/%s/_search", baseURL, index), strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if user := os.Getenv("ES_APM_VERIFY_USER"); user != "" {
		httpReq.SetBasicAuth(user, os.Getenv("ES_APM_VERIFY_PASS"))
	}

	// Self-signed cert on the in-cluster endpoint; this harness only ever talks to a
	// port-forward the operator opened themselves.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("query cluster: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cluster returned %d: %s", resp.StatusCode, string(raw))
	}

	var searchResp esTraceSearchResponse
	if err := json.Unmarshal(raw, &searchResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(searchResp.Hits.Hits) == 0 {
		t.Fatalf("no documents in %s — point ES_APM_VERIFY_INDEX at a stream that has traces", index)
	}
	t.Logf("fetched %d hits from %s", len(searchResp.Hits.Hits), index)

	// Run every hit through the production mapper and count how many produced each
	// canonical field. A field at 0 is a wrong path; the unit tests cannot see that
	// because they feed the mapper documents written to match it.
	populated := map[string]int{}
	kinds := map[string]int{}
	var transactions, spans, withDest int
	for _, hit := range searchResp.Hits.Hits {
		tr := mapAPMHitToTrace(hit.Source)
		kinds[tr.SpanKind]++
		switch tr.SpanKind {
		case "Server":
			transactions++
		case "Client":
			spans++
		}
		for name, v := range map[string]string{
			"trace_id":           tr.TraceID,
			"span_id":            tr.SpanID,
			"parent_span_id":     tr.ParentSpanID,
			"span_name":          tr.SpanName,
			"service_name":       tr.ServiceName,
			"timestamp":          tr.Timestamp,
			"status_code":        tr.StatusCode,
			"workload_namespace": tr.WorkloadNamespace,
			"resource":           tr.Resource,
			"http_status_code":   tr.HTTPStatusCode,
			"destination_name":   tr.DestinationName,
		} {
			if v != "" {
				populated[name]++
			}
		}
		if tr.DurationNs > 0 {
			populated["duration_ns"]++
		}
		if tr.ResourceAttributes["kubernetes.pod.name"] != "" {
			populated["kubernetes.pod.name"]++
		}
		if tr.DestinationName != "" {
			withDest++
		}
	}

	total := len(searchResp.Hits.Hits)
	t.Logf("span kinds: %v", kinds)
	t.Logf("transactions=%d spans=%d with-destination=%d", transactions, spans, withDest)
	for _, f := range []string{
		"trace_id", "span_id", "parent_span_id", "span_name", "service_name", "timestamp",
		"status_code", "duration_ns", "workload_namespace", "resource", "http_status_code",
		"destination_name", "kubernetes.pod.name",
	} {
		t.Logf("  %-22s %4d/%d", f, populated[f], total)
	}

	// These must hold on any Elastic APM stream, whatever is instrumented. They are
	// the fields whose absence would make the Traces tab unusable.
	for _, f := range []string{"trace_id", "span_id", "span_name", "service_name", "timestamp", "duration_ns", "status_code"} {
		if populated[f] == 0 {
			t.Errorf("canonical field %q came back empty for all %d documents — the Elastic APM field path is wrong", f, total)
		}
	}

	// Both document kinds must map. Seeing only one means the coalescing in
	// mapAPMHitToTrace regressed to handling a single processor.event.
	if transactions == 0 {
		t.Errorf("no transaction documents mapped (processor.event=transaction); got kinds %v", kinds)
	}
	if spans == 0 {
		t.Errorf("no span documents mapped (processor.event=span); got kinds %v", kinds)
	}

	// Duration sanity: APM stores microseconds and the mapper scales to nanoseconds.
	// A pass-through bug makes every span look ~1000x faster, which no assertion on
	// presence alone would catch, so check the magnitude is plausible for a real
	// request (>= 1µs == 1000ns).
	var maxNs int64
	for _, hit := range searchResp.Hits.Hits {
		if d := mapAPMHitToTrace(hit.Source).DurationNs; d > maxNs {
			maxNs = d
		}
	}
	t.Logf("max duration: %dns (%.3fms)", maxNs, float64(maxNs)/1e6)
	if maxNs > 0 && maxNs < 1000 {
		t.Errorf("max duration %dns is implausibly small — microseconds are probably being passed through unscaled", maxNs)
	}
}
