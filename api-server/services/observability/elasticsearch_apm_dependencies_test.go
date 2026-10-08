package observability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPMElasticsearch answers the four Elastic APM reads with canned bodies shaped
// like APM Server 8.19's responses, and records every request body it saw.
type fakeAPMElasticsearch struct {
	mu       sync.Mutex
	requests []map[string]any
	paths    []string
	// noDestinations makes read 1 return no buckets (the probe's "no APM data").
	noDestinations bool
}

func (f *fakeAPMElasticsearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()

	s := string(raw)
	switch {
	case strings.HasPrefix(r.URL.Path, "/metrics-apm"):
		if f.noDestinations {
			_, _ = io.WriteString(w, `{"aggregations":{"grouped":{"buckets":[]}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"aggregations":{"grouped":{"buckets":[
			{"key":{"service":"frontend","resource":"product-catalog:8080"},"doc_count":3,
			 "calls":{"value":7320},"latency_sum":{"value":456768000},
			 "failures":{"doc_count":1,"calls":{"value":12}},
			 "target_type":{"buckets":[{"key":"grpc"}]},"target_name":{"buckets":[]}},
			{"key":{"service":"cart","resource":"redis"},"doc_count":2,
			 "calls":{"value":34658},"latency_sum":{"value":24271134},
			 "failures":{"doc_count":0,"calls":{"value":0}},
			 "target_type":{"buckets":[{"key":"redis"}]},"target_name":{"buckets":[]}}
		]}}}`)
	case strings.Contains(s, `"samples"`):
		_, _ = io.WriteString(w, `{"aggregations":{"grouped":{"buckets":[
			{"key":{"service":"frontend","namespace":"demo","pod":"frontend-aaa-1","resource":"product-catalog:8080"},"doc_count":9,
			 "samples":{"hits":{"hits":[{"_source":{"span":{"id":"s1"}}},{"_source":{"span":{"id":"s2"}}}]}}},
			{"key":{"service":"cart","namespace":"demo","pod":"cart-aaa-1","resource":"redis"},"doc_count":4,
			 "samples":{"hits":{"hits":[{"_source":{"span":{"id":"s3"}}}]}}},
			{"key":{"service":"cart","namespace":"demo","pod":"cart-aaa-1","resource":"flagd:8013"},"doc_count":4,
			 "samples":{"hits":{"hits":[{"_source":{"span":{"id":"s4"}}}]}}}
		]}}}`)
	case strings.Contains(s, `"parents"`):
		// s1 and s2 land in product-catalog; s4's child is cart's own in-process
		// span, which is not a receiver; s3 (redis) has no child at all.
		_, _ = io.WriteString(w, `{"aggregations":{"parents":{"buckets":[
			{"key":"s1","receiver":{"hits":{"hits":[{"_source":{"service":{"name":"product-catalog"},"kubernetes":{"namespace":"demo","pod":{"name":"product-catalog-aaa-1"}}}}]}}},
			{"key":"s2","receiver":{"hits":{"hits":[{"_source":{"service":{"name":"product-catalog"},"kubernetes":{"namespace":"demo","pod":{"name":"product-catalog-aaa-1"}}}}]}}},
			{"key":"s4","receiver":{"hits":{"hits":[{"_source":{"service":{"name":"cart"},"kubernetes":{"namespace":"demo","pod":{"name":"cart-aaa-1"}}}}]}}}
		]}}}`)
	case strings.Contains(s, `"last_seen"`):
		_, _ = io.WriteString(w, `{"aggregations":{"grouped":{"buckets":[
			{"key":{"service":"frontend","namespace":"demo","pod":"frontend-aaa-1"},"doc_count":5,"last_seen":{"value":1791460800000}},
			{"key":{"service":"cart","namespace":null,"pod":"cart-aaa-1"},"doc_count":5,"last_seen":{"value":1791460800000}}
		]}}}`)
	default:
		http.Error(w, "unexpected request: "+s, http.StatusBadRequest)
	}
}

func newFakeAPMES(t *testing.T) (*fakeAPMElasticsearch, *ElasticsearchConfig) {
	t.Helper()
	fake := &fakeAPMElasticsearch{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, &ElasticsearchConfig{Url: srv.URL, Username: "u", Password: "p", AuthType: "basic", IntegrationID: "es-1"}
}

var apmTestEnd = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func fetchTestAPM(t *testing.T, cfg *ElasticsearchConfig) (*ElasticAPMDependencies, error) {
	t.Helper()
	ctx := security.NewRequestContextForTenantAdmin("", nil, nil, nil)
	return FetchElasticAPMDependencies(ctx, cfg,
		apmTestEnd.Add(-24*time.Hour), apmTestEnd.Add(-time.Hour), apmTestEnd)
}

func TestFetchElasticAPMDependencies_JoinsTheFourReads(t *testing.T) {
	fake, cfg := newFakeAPMES(t)
	got, err := fetchTestAPM(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, []ElasticAPMDependency{
		{CallerService: "frontend", Resource: "product-catalog:8080", TargetType: "grpc", Calls: 7320, Failures: 12, LatencySumUs: 456768000},
		{CallerService: "cart", Resource: "redis", TargetType: "redis", Calls: 34658, LatencySumUs: 24271134},
	}, got.Dependencies)

	assert.Equal(t, []ElasticAPMCall{
		{CallerService: "frontend", CallerNamespace: "demo", CallerPod: "frontend-aaa-1", Resource: "product-catalog:8080",
			ReceiverService: "product-catalog", ReceiverNamespace: "demo", ReceiverPod: "product-catalog-aaa-1"},
		// No receiver found: still emitted so the caller's ns/pod reach the flow source.
		{CallerService: "cart", CallerNamespace: "demo", CallerPod: "cart-aaa-1", Resource: "redis"},
		// The only child is cart's own span: not a hop to another service.
		{CallerService: "cart", CallerNamespace: "demo", CallerPod: "cart-aaa-1", Resource: "flagd:8013"},
	}, got.Calls)

	require.Len(t, got.Pods, 2)
	assert.Equal(t, ElasticAPMServicePod{Service: "frontend", Namespace: "demo", Pod: "frontend-aaa-1",
		LastSeen: time.UnixMilli(1791460800000).UTC()}, got.Pods[0])
	assert.Equal(t, "", got.Pods[1].Namespace, "a missing namespace bucket key is empty, not dropped")

	// Read 1 hits the metrics index and excludes the 10m/60m rollups.
	require.Len(t, fake.paths, 4)
	assert.Equal(t, "/metrics-apm*/_search", fake.paths[0])
	read1, _ := json.Marshal(fake.requests[0])
	assert.Contains(t, string(read1), `"metricset.name":"service_destination"`)
	assert.Contains(t, string(read1), `"metricset.interval":["10m","60m"]`)
	// The receiver read carries exactly the sampled span ids.
	read3, _ := json.Marshal(fake.requests[2])
	assert.Contains(t, string(read3), `"parent.id":["s1","s2","s3","s4"]`)
}

func TestFetchElasticAPMDependencies_NoDestinationsIsNoAPM(t *testing.T) {
	fake, cfg := newFakeAPMES(t)
	fake.noDestinations = true
	got, err := fetchTestAPM(t, cfg)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Len(t, fake.paths, 1, "the probe stops before touching raw trace documents")
}

func TestFetchElasticAPMDependencies_HonoursCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })

	cctx, cancel := context.WithCancel(context.Background())
	ctx := security.NewRequestContext(cctx, nil, nil, nil, nil)
	cfg := &ElasticsearchConfig{Url: srv.URL, Username: "u", Password: "p", AuthType: "basic"}
	done := make(chan error, 1)
	go func() {
		_, err := FetchElasticAPMDependencies(ctx, cfg, apmTestEnd.Add(-time.Hour), apmTestEnd, apmTestEnd)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled build must not wait for the 30s client timeout")
	}
}

func TestJoinElasticAPMCalls_OneCallPerReceiverOrOneWithout(t *testing.T) {
	var relay elasticAPMDoc
	relay.Service.Name = "relay-server"
	relay.Kubernetes.Namespace = "nudgebee"
	relay.Kubernetes.Pod.Name = "relay-server-1"
	var self elasticAPMDoc
	self.Service.Name = "services-server"

	got := joinElasticAPMCalls([]elasticAPMCallSample{
		// The API pod's call was received by relay-server.
		{service: "services-server", namespace: "nudgebee", pod: "services-server-1", resource: "relay-server:8080", spanIDs: []string{"a", "b"}},
		// The worker pod (same service name) found no receiver, and its only child
		// is in-process: it is still a caller in its own right.
		{service: "services-server", namespace: "nudgebee", pod: "services-server-worker-1", resource: "relay-server:8080", spanIDs: []string{"c"}},
	}, map[string]elasticAPMDoc{"a": relay, "b": relay, "c": self})

	assert.Equal(t, []ElasticAPMCall{
		{CallerService: "services-server", CallerNamespace: "nudgebee", CallerPod: "services-server-1", Resource: "relay-server:8080",
			ReceiverService: "relay-server", ReceiverNamespace: "nudgebee", ReceiverPod: "relay-server-1"},
		{CallerService: "services-server", CallerNamespace: "nudgebee", CallerPod: "services-server-worker-1", Resource: "relay-server:8080"},
	}, got)
}

func TestFetchElasticAPMDependencies_ReadsTheConfiguredTraceIndex(t *testing.T) {
	fake, cfg := newFakeAPMES(t)
	cfg.TraceIndex = "apm-prod-traces*"
	_, err := fetchTestAPM(t, cfg)
	require.NoError(t, err)
	require.Len(t, fake.paths, 4)
	assert.Equal(t, "/metrics-apm*/_search", fake.paths[0], "no setting declares the metrics index")
	for _, p := range fake.paths[1:] {
		assert.Equal(t, "/apm-prod-traces*/_search", p)
	}
}

func TestFetchElasticAPMDependencies_BatchesReceiverLookups(t *testing.T) {
	orig := elasticAPMReceiverBatchSize
	elasticAPMReceiverBatchSize = 3
	t.Cleanup(func() { elasticAPMReceiverBatchSize = orig })

	fake, cfg := newFakeAPMES(t)
	got, err := fetchTestAPM(t, cfg)
	require.NoError(t, err)

	var batches []string
	for _, req := range fake.requests {
		b, _ := json.Marshal(req)
		if strings.Contains(string(b), `"parents"`) {
			batches = append(batches, string(b))
		}
	}
	require.Len(t, batches, 2, "4 span ids at 3 per request")
	assert.Contains(t, batches[0], `"parent.id":["s1","s2","s3"]`)
	assert.Contains(t, batches[1], `"parent.id":["s4"]`)
	// The merged result is the same as one request's.
	assert.Equal(t, "product-catalog", got.Calls[0].ReceiverService)
}
