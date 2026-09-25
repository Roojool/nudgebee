package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"nudgebee/relay-server/pkg/config"
)

// recordingRPCClient records the routing key of every Call.
type recordingRPCClient struct {
	mu   sync.Mutex
	keys []string
}

func (r *recordingRPCClient) Call(_ context.Context, _, routingKey string, _ []byte, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, routingKey)
	return []byte(`{"ok":true}`), nil
}
func (r *recordingRPCClient) InstanceID() string               { return "test" }
func (r *recordingRPCClient) DeliverLocal(string, []byte) bool { return false }
func (r *recordingRPCClient) AbandonCh(string) <-chan struct{} { return nil }
func (r *recordingRPCClient) Close()                           {}
func (r *recordingRPCClient) routingKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

// fakeProber answers HasConsumers from a fixed set of consumed queues.
type fakeProber struct {
	consumed map[string]bool
	err      error
}

func (f *fakeProber) HasConsumers(_ context.Context, queue string) (bool, error) {
	return f.consumed[queue], f.err
}

func routingLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func proxyRequest(dsID string) []byte {
	return []byte(fmt.Sprintf(`{"body":{"account_id":"acct-1","action_name":"ssh_command","action_params":{"datasource_id":%q,"params":{"command":"uname -a"}}}}`, dsID))
}

func TestProxyRoutingKey(t *testing.T) {
	const (
		shared = "relay_requests_acct-1_proxy"
		queueA = "relay_requests_acct-1_proxy_agent-a"
	)
	owners := map[string]string{"local:fleet:ssh": "agent-a"}
	bothConsumed := &fakeProber{consumed: map[string]bool{queueA: true, shared: true}}

	cases := []struct {
		name   string
		store  *fakeStore
		prober *fakeProber
		body   []byte
		want   string
	}{
		{"owned datasource goes to the owner's queue", &fakeStore{datasourceOwners: owners}, bothConsumed, proxyRequest("local:fleet:ssh"), queueA},
		{"unified top-level datasource_id is honoured", &fakeStore{datasourceOwners: owners}, bothConsumed,
			[]byte(`{"account_id":"acct-1","action":"ssh_command","datasource_id":"local:fleet:ssh","params":{}}`), queueA},
		{"unknown owner (legacy row) falls back to shared", &fakeStore{datasourceOwners: owners}, bothConsumed, proxyRequest("local:legacy"), shared},
		{"request without a datasource falls back to shared", &fakeStore{datasourceOwners: owners}, bothConsumed,
			[]byte(`{"body":{"account_id":"acct-1","action_name":"resync_inventory"}}`), shared},
		{"owner lookup error falls back to shared", &fakeStore{datasourceOwners: owners, ownerErr: errors.New("db down")}, bothConsumed, proxyRequest("local:fleet:ssh"), shared},
		{"owner queue without consumer (rollout / offline owner) falls back to shared", &fakeStore{datasourceOwners: owners},
			&fakeProber{consumed: map[string]bool{shared: true}}, proxyRequest("local:fleet:ssh"), shared},
		{"probe error falls back to shared", &fakeStore{datasourceOwners: owners}, &fakeProber{err: errors.New("broker")}, proxyRequest("local:fleet:ssh"), shared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := proxyRoutingKey(context.Background(), tc.store, tc.prober, "acct-1", tc.body, routingLogger())
			assert.Equal(t, tc.want, got)
		})
	}
}

// Two proxy agents on one account with disjoint datasources: every request
// must be published to the queue of the agent that owns its datasource
// (spec D2 acceptance: 50 requests, 0 misroutes).
func TestRequestHandler_RoutesProxyRequestsToOwningAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeStore{
		allowed: true,
		datasourceOwners: map[string]string{
			"local:a-db": "agent-a",
			"local:b-db": "agent-b",
		},
	}
	prober := &fakeProber{consumed: map[string]bool{
		"relay_requests_acct-1_proxy_agent-a": true,
		"relay_requests_acct-1_proxy_agent-b": true,
		"relay_requests_acct-1_proxy":         true,
	}}
	rpc := &recordingRPCClient{}
	cfg := &config.Config{}
	cfg.HTTP.WriteTimeout = time.Second
	cfg.RabbitMQ.ExchangeName = "exch"
	tr := tracenoop.NewTracerProvider().Tracer("test")
	meter := noop.NewMeterProvider().Meter("test")
	r := gin.New()
	r.POST("/request", NewRequestHandler(store, rpc, &fakeEnsurer{}, prober, cfg, nil, &tr, &meter, routingLogger()))

	var want []string
	for i := 0; i < 50; i++ {
		ds, owner := "local:a-db", "agent-a"
		if i%2 == 1 {
			ds, owner = "local:b-db", "agent-b"
		}
		want = append(want, "relay_requests_acct-1_proxy_"+owner)

		req := httptest.NewRequest("POST", "/request", bytes.NewReader(proxyRequest(ds)))
		req.Header.Set("X-NB-Agent-Type", "proxy")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, w.Body.String())
	}

	assert.Equal(t, want, rpc.routingKeys())
}

// k8s-agent requests never consult datasource ownership.
func TestRequestHandler_K8sRequestsKeepAccountQueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeStore{allowed: true, datasourceOwners: map[string]string{"local:a-db": "agent-a"}}
	rpc := &recordingRPCClient{}
	cfg := &config.Config{}
	cfg.HTTP.WriteTimeout = time.Second
	tr := tracenoop.NewTracerProvider().Tracer("test")
	meter := noop.NewMeterProvider().Meter("test")
	r := gin.New()
	r.POST("/request", NewRequestHandler(store, rpc, &fakeEnsurer{}, &fakeProber{}, cfg, nil, &tr, &meter, routingLogger()))

	req := httptest.NewRequest("POST", "/request", bytes.NewReader(proxyRequest("local:a-db")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	assert.Equal(t, []string{"relay_requests_acct-1"}, rpc.routingKeys())
}
