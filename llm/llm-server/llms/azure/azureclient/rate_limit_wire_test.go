package azureclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/llm/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure builds its URL differently (deployment + api-version) and has its own
// non-2xx block, so it needs its own wire check rather than relying on the
// OpenAI client's.
func newWireClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New("test-token", "gpt-4", "", srv.URL, APITypeAzure, "2024-02-01", srv.Client())
	require.NoError(t, err)
	return c
}

func TestAzureChat429CarriesHeaders(t *testing.T) {
	c := newWireClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "39")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "0")
		w.Header().Set("Api-Key", "must-not-be-captured")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"429","message":"Requests to the ChatCompletions_Create Operation have exceeded token rate limit of your current OpenAI S0 pricing tier."}}`))
	})

	_, err := c.CreateChat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []*ChatMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API returned unexpected status code: 429")
	assert.Contains(t, err.Error(), "token rate limit")

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok)
	assert.Equal(t, "39", headers["retry-after"])
	assert.Equal(t, "0", headers["x-ratelimit-remaining-tokens"])
	_, leaked := headers["api-key"]
	assert.False(t, leaked)

	// Azure names the other dimension in passing ("Requests to the ... Operation"),
	// so this is the case that must resolve to tokens, not requests.
	assert.Equal(t, common.RateLimitScopeTokens, common.RateLimitDetailFromError(err).Scope)
}

func TestAzureEmbeddings429CarriesHeaders(t *testing.T) {
	c := newWireClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "6")
		w.Header().Set("X-RateLimit-Remaining-Requests", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"requests-per-minute (RPM) limit reached"}}`))
	})

	_, err := c.CreateEmbedding(context.Background(), &EmbeddingRequest{Model: "text-embedding-ada-002", Input: []string{"hello"}})
	require.Error(t, err)

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok)
	assert.Equal(t, "6", headers["retry-after"])

	detail := common.RateLimitDetailFromError(err)
	assert.Equal(t, common.RateLimitScopeRequests, detail.Scope)
	assert.Equal(t, common.RateLimitWindowMinute, detail.Window)
}

func TestAzureNon429AcquiresNoReason(t *testing.T) {
	c := newWireClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining-Tokens", "0")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens."}}`))
	})

	_, err := c.CreateChat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []*ChatMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.False(t, common.RateLimitDetailFromError(err).Known(),
		"a context-overflow 400 must not be labelled a token rate limit")
}
