package openaiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/llm/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercises the real non-2xx path over a real connection: the rate-limit headers
// have to survive from the wire into the returned error. Every other test in this
// change constructs the error directly, which would not catch the client failing
// to pass r.Header through.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New("test-token", "gpt-4", srv.URL, "", APITypeOpenAI, "", srv.Client(), "text-embedding-3-small", nil)
	require.NoError(t, err)
	return c
}

func TestChat429CarriesHeadersFromTheWire(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.Header().Set("X-RateLimit-Limit-Requests", "10")
		w.Header().Set("X-RateLimit-Remaining-Requests", "0")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "48000")
		w.Header().Set("Authorization", "Bearer must-not-be-logged")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit is exceeded. This deployment has reached its configured requests-per-minute (RPM) limit.","type":"rate_limit_error"}}`))
	})

	_, err := c.CreateChat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []*ChatMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	// Legacy wording preserved — the retry/fallback classifiers match on it.
	assert.Contains(t, err.Error(), "API returned unexpected status code: 429")
	assert.Contains(t, err.Error(), "requests-per-minute (RPM) limit")

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok, "headers must survive the wire")
	assert.Equal(t, "17", headers["retry-after"])
	assert.Equal(t, "10", headers["x-ratelimit-limit-requests"])
	assert.Equal(t, "0", headers["x-ratelimit-remaining-requests"])
	assert.Equal(t, "48000", headers["x-ratelimit-remaining-tokens"])
	_, leaked := headers["authorization"]
	assert.False(t, leaked, "credential headers must never be captured")

	detail := common.RateLimitDetailFromError(err)
	assert.Equal(t, common.RateLimitScopeRequests, detail.Scope)
	assert.Equal(t, common.RateLimitWindowMinute, detail.Window)
}

// Gateways return HTML or plain text on a 429 as often as JSON. The decode fails,
// and the error must still carry the status and the headers.
func TestChat429WithUndecodableBodyStillCarriesHeaders(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`<html><head><title>429 Too Many Requests</title></head><body><h1>429</h1></body></html>`))
	})

	_, err := c.CreateChat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []*ChatMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	// Body did not decode, so the message is the status alone — as errors.New(msg)
	// produced before this change.
	assert.Equal(t, "API returned unexpected status code: 429", err.Error())

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok, "an undecodable body must not cost us the headers")
	assert.Equal(t, "30", headers["retry-after"])

	// The body said nothing, but the headers name the dimension.
	assert.Equal(t, common.RateLimitScopeTokens, common.RateLimitDetailFromError(err).Scope)
}

// A streaming request is rejected before any chunk arrives, so it takes the same
// non-2xx branch. The log this change came from was a streaming call.
func TestStreamingChat429TakesTheSamePath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining-Requests", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"requests-per-minute (RPM) limit reached"}}`))
	})

	streamed := 0
	_, err := c.CreateChat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []*ChatMessage{{Role: "user", Content: "hi"}},
		StreamingFunc: func(_ context.Context, _ []byte) error {
			streamed++
			return nil
		},
	})
	require.Error(t, err)
	assert.Zero(t, streamed, "no chunk should be delivered for a rejected stream")

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok)
	assert.Equal(t, "0", headers["x-ratelimit-remaining-requests"])
}

// The embeddings client was swept in the same change and must behave identically.
func TestEmbeddings429CarriesHeaders(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"tokens per min (TPM) limit reached"}}`))
	})

	_, err := c.CreateEmbedding(context.Background(), &EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: []string{"hello"},
	})
	require.Error(t, err)

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok)
	assert.Equal(t, "5", headers["retry-after"])

	detail := common.RateLimitDetailFromError(err)
	assert.Equal(t, common.RateLimitScopeTokens, detail.Scope)
	assert.Equal(t, common.RateLimitWindowMinute, detail.Window)
}

// A non-429 failure must keep the legacy message shape and acquire no reason.
func TestNon429ResponsesUnchangedOverTheWire(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining-Requests", "0")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		})

		_, err := c.CreateChat(context.Background(), &ChatRequest{
			Model:    "gpt-4",
			Messages: []*ChatMessage{{Role: "user", Content: "hi"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")

		var provErr *common.ProviderHTTPError
		require.True(t, errors.As(err, &provErr))
		assert.Equal(t, status, provErr.StatusCode)
		assert.False(t, common.RateLimitDetailFromError(err).Known(),
			"status %d must not carry a rate-limit reason", status)
	}
}
