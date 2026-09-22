package anthropicclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"nudgebee/llm/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Anthropic words its errors differently from the OpenAI client, so it carries
// rate-limit headers via common.WithRateLimitHeaders. This checks the real wire
// path: headers survive, and the message is byte-for-byte what it always was —
// the retry and fallback classifiers match on that text.
func newWireClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New("test-token", "claude-3-sonnet", srv.URL, srv.Client())
	require.NoError(t, err)
	return c
}

func TestAnthropic429CarriesHeadersAndKeepsWording(t *testing.T) {
	c := newWireClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "21")
		w.Header().Set("Anthropic-RateLimit-Requests-Remaining", "0")
		w.Header().Set("Anthropic-RateLimit-Requests-Limit", "50")
		w.Header().Set("X-Api-Key", "must-not-be-captured")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your per-minute rate limit."}}`))
	})

	_, err := c.CreateMessage(context.Background(), &MessageRequest{
		Model:     "claude-3-sonnet",
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 16,
	})
	require.Error(t, err)

	// Unchanged wording.
	assert.Equal(t,
		"anthropic: API error (429 rate_limit_error): Number of requests has exceeded your per-minute rate limit.",
		err.Error())

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok, "headers must survive the wire")
	assert.Equal(t, "21", headers["retry-after"])
	assert.Equal(t, "0", headers["anthropic-ratelimit-requests-remaining"])
	assert.Equal(t, "50", headers["anthropic-ratelimit-requests-limit"])
	_, leaked := headers["x-api-key"]
	assert.False(t, leaked, "credential headers must never be captured")

	detail := common.RateLimitDetailFromError(err)
	assert.Equal(t, common.RateLimitScopeRequests, detail.Scope)
	assert.Equal(t, common.RateLimitWindowMinute, detail.Window)
}

// An undecodable body takes the other branch; it too must keep its wording.
func TestAnthropic429UndecodableBodyKeepsWording(t *testing.T) {
	c := newWireClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`<html>429</html>`))
	})

	_, err := c.CreateMessage(context.Background(), &MessageRequest{
		Model:     "claude-3-sonnet",
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 16,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic: API returned status code 429")

	headers, ok := common.RateLimitHeadersFromError(err)
	require.True(t, ok)
	assert.Equal(t, "60", headers["retry-after"])
}

func TestAnthropicNon429AcquiresNoReason(t *testing.T) {
	c := newWireClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Anthropic-RateLimit-Requests-Remaining", "0")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"Internal server error"}}`))
	})

	_, err := c.CreateMessage(context.Background(), &MessageRequest{
		Model:     "claude-3-sonnet",
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 16,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Internal server error")
	assert.False(t, common.RateLimitDetailFromError(err).Known(),
		"a 500 must not pick up a rate-limit reason")
}
