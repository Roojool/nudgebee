package observability

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
)

// The retry budget belongs to the caller, because the same keys serve opposite needs: a
// UI chart would rather show an error than hang, while an hourly anomaly run can afford
// to wait a rate limit out. These pin that the caller chooses and the layer clamps.

func TestDatadogRetryBudget_ClampsWhatTheCallerAsksFor(t *testing.T) {
	// Unset means the short default -- the UI's need, since most callers are charts.
	assert.Equal(t, datadogRetryBudgetDefault, datadogRetryBudget(0))
	assert.Equal(t, datadogRetryBudgetDefault, datadogRetryBudget(-5))

	// A background job asking for longer gets it, up to the ceiling.
	assert.Equal(t, 10*time.Second, datadogRetryBudget(10))

	// The ceiling exists because the budget arrives over the wire: without it a caller
	// could pin a connection for as long as it liked.
	assert.Equal(t, datadogRetryBudgetMax, datadogRetryBudget(3600))
}

// A rate-limited provider must not hold a UI request open. With no budget to spend, the
// first 429 comes straight back as an error instead of sleeping.
func TestDatadogRetry_GivesUpInsteadOfBlowingTheBudget(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "120") // far beyond any budget
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":["rate limited"]}`))
	}))
	defer server.Close()

	ctx := security.NewRequestContextForSuperAdmin(nil, nil, nil)
	start := time.Now()
	_, err := fetchDatadogAPI(ctx, server.URL, map[string]string{}, time.Now().Add(1*time.Second))
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "budget", "the error must say why it stopped trying")
	assert.Less(t, elapsed, 5*time.Second,
		"a 120s Retry-After must not be honoured when the budget is 1s")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"no point retrying when the wait would exceed the budget")
}

// The budget bounds the WAIT, not the work: a provider that answers immediately is
// unaffected by a small budget.
func TestDatadogRetry_SmallBudgetDoesNotBreakAHealthyProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	ctx := security.NewRequestContextForSuperAdmin(nil, nil, nil)
	// A deadline already past means "no waiting at all" -- and still succeeds, because
	// the budget bounds retrying, not the request.
	body, err := fetchDatadogAPI(ctx, server.URL, map[string]string{}, time.Now())
	assert.NoError(t, err)
	assert.JSONEq(t, `{"data":[]}`, string(body))
}

// The budget bounds a REQUEST, not each key inside it.
//
// fetchDatadogAPI takes a deadline rather than a duration precisely so several keys can
// share one. Given a duration it would restart the clock per key, and a rate-limited
// Datadog would be waited out once per key -- ten keys at the 30s ceiling is five
// minutes of pinned connection, which is what the budget exists to prevent.
func TestDatadogRetry_OneDeadlineBoundsEveryKeyNotEachKey(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":["rate limited"]}`))
	}))
	defer server.Close()

	ctx := security.NewRequestContextForSuperAdmin(nil, nil, nil)
	const keys = 10
	budget := 2 * time.Second

	// What the metrics loop does: compute the deadline ONCE, then reuse it per key.
	deadline := time.Now().Add(budget)
	start := time.Now()
	for i := 0; i < keys; i++ {
		_, err := fetchDatadogAPI(ctx, server.URL, map[string]string{}, deadline)
		assert.Error(t, err, "every key still gets its own error; none is silent")
	}
	elapsed := time.Since(start)

	// Per-key budgets would allow keys*budget here. One shared deadline does not.
	assert.Less(t, elapsed, budget+2*time.Second,
		"ten rate-limited keys must not cost ten budgets")
	assert.Greater(t, int32(keys), int32(0))
}
