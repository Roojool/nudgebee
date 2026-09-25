package mq

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Proxy agents get a queue per agent; everything else keeps the per-account
// queue, so k8s routing is untouched by per-agent queues.
func TestRelayAgentQueueName(t *testing.T) {
	assert.Equal(t, "relay_requests_acct-1_proxy_agent-a", RelayAgentQueueName("acct-1", "proxy", "agent-a"))
	assert.Equal(t, RelayQueueName("acct-1", "proxy"), RelayAgentQueueName("acct-1", "proxy", ""),
		"an unknown agent id must fall back to the shared proxy queue")
	assert.Equal(t, RelayQueueName("acct-1", "k8s"), RelayAgentQueueName("acct-1", "k8s", "agent-k"))
	assert.Equal(t, RelayQueueName("acct-1", ""), RelayAgentQueueName("acct-1", "", "agent-k"))
}

// Forgetting one agent's queue must not drop the shared queue's record, which
// the same session also consumes.
func TestForgetQueueIsScopedToOneQueue(t *testing.T) {
	topo := &Topology{}
	own := RelayAgentQueueName("acct-1", "proxy", "agent-a")
	shared := RelayQueueName("acct-1", "proxy")
	topo.declared.Store(own, settled(nil))
	topo.declared.Store(shared, settled(nil))

	topo.ForgetQueue(own)

	require.Equal(t, []string{shared}, declaredKeys(topo))
}

// A fresh cached probe answers without touching the broker; a stale one is
// not trusted. The connection manager here has no connection, so any broker
// round-trip fails with the (already cancelled) context's error.
func TestHasConsumersUsesFreshCacheOnly(t *testing.T) {
	now := time.Now()
	topo := &Topology{connMgr: &ConnectionManager{}, now: func() time.Time { return now }}
	topo.consumerProbes.Store("q-fresh", consumerProbe{has: true, at: now.Add(-time.Second)})

	has, err := topo.HasConsumers(canceledCtx(), "q-fresh")
	require.NoError(t, err)
	assert.True(t, has)

	topo.consumerProbes.Store("q-stale", consumerProbe{has: true, at: now.Add(-2 * consumerProbeTTL)})
	_, err = topo.HasConsumers(canceledCtx(), "q-stale")
	assert.ErrorIs(t, err, context.Canceled, "a stale probe must go back to the broker")
}

// The payload must match llm-server's consumer (cacheInvalidationMessage,
// field account_ids) and api-server's publisher.
func TestLLMCacheInvalidationPayloadWireFormat(t *testing.T) {
	b, err := json.Marshal(LLMCacheInvalidationPayload{AccountIDs: []string{"acct-1"}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"account_ids":["acct-1"]}`, string(b))
}

func TestNewLLMCacheInvalidatorDisabledWithoutExchange(t *testing.T) {
	inv := NewLLMCacheInvalidator(&ConnectionManager{}, "", nil)
	_, isNoop := inv.(noopLLMCacheInvalidator)
	assert.True(t, isNoop)
	inv.InvalidateAccounts("acct-1") // must not panic or block
}

func TestDedupeNonEmpty(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, dedupeNonEmpty([]string{"", "a", "b", "a", ""}))
	assert.Empty(t, dedupeNonEmpty([]string{""}))
}

// A session that ends must stop this replica routing to its per-agent queue
// immediately, not after the cached probe ages out.
func TestForgetConsumerProbe(t *testing.T) {
	topo := &Topology{}
	topo.consumerProbes.Store("q-agent", consumerProbe{has: true, at: time.Now()})
	topo.consumerProbes.Store("q-other", consumerProbe{has: true, at: time.Now()})

	topo.ForgetConsumerProbe("q-agent")

	_, agentCached := topo.consumerProbes.Load("q-agent")
	_, otherCached := topo.consumerProbes.Load("q-other")
	assert.False(t, agentCached)
	assert.True(t, otherCached)
}

// x-expires is sent as an int32 millisecond count; 24h must fit.
func TestAgentQueueExpiryFitsInt32(t *testing.T) {
	ms := agentQueueExpiry.Milliseconds()
	assert.Equal(t, int64(int32(ms)), ms)
	assert.Positive(t, ms)
}
