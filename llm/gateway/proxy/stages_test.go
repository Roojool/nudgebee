package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"nudgebee/llm-gateway/auth"
	"nudgebee/llm-gateway/ratelimit"
	"nudgebee/llm-gateway/routing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLimitSource mimics the EE store's per-key/per-model indexing (task 4): it
// only returns a limit when the scope passed in matches the limit's own key/model.
// That is the layer that actually enforces per-key isolation — Check's own guard
// only re-checks Model — so this is what proves ratelimitStage.Handle threads
// rc.Identity.APIKeyID and rc.Model into the Check call.
type fakeLimitSource struct{ limits []ratelimit.Limit }

func (f fakeLimitSource) LimitsFor(tenant, user, apiKey, model string) []ratelimit.Limit {
	var out []ratelimit.Limit
	for _, lim := range f.limits {
		if lim.TenantID != "" && lim.TenantID != tenant {
			continue
		}
		if lim.UserID != "" && lim.UserID != user {
			continue
		}
		if lim.APIKeyID != "" && lim.APIKeyID != apiKey {
			continue
		}
		if lim.Model != "" && lim.Model != model {
			continue
		}
		out = append(out, lim)
	}
	return out
}

func newRateLimitTestContext(identity auth.Identity, model string) *RequestContext {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	return &RequestContext{
		Gin:      c,
		Ctx:      context.Background(),
		Identity: identity,
		Model:    model,
	}
}

// TestRatelimitStage_ScopesToAPIKeyAndRequestedModel locks the wiring for task 5:
// a limit configured for one (tenant, user, api key, model) tuple must trip only
// for requests carrying that exact key and the model the CLIENT asked for — not
// merely the tenant/user, and not a different key or a different requested model.
func TestRatelimitStage_ScopesToAPIKeyAndRequestedModel(t *testing.T) {
	limiter := ratelimit.New(ratelimit.NewMemStore(), fakeLimitSource{[]ratelimit.Limit{
		{TenantID: "t", UserID: "u", APIKeyID: "key-1", Model: "gpt-5",
			Metric: ratelimit.MetricRequests, Period: ratelimit.PeriodMinute, Value: 1, Enabled: true},
	}})
	s := ratelimitStage{limiter: limiter}

	// First request against key-1/gpt-5 consumes the one allowed slot.
	rc := newRateLimitTestContext(auth.Identity{TenantID: "t", UserID: "u", APIKeyID: "key-1"}, "gpt-5")
	stop, err := s.Handle(rc)
	require.NoError(t, err)
	assert.False(t, stop, "first request under the cap should pass")

	// Second request, same key + model, must be rejected (429).
	rc2 := newRateLimitTestContext(auth.Identity{TenantID: "t", UserID: "u", APIKeyID: "key-1"}, "gpt-5")
	stop2, err := s.Handle(rc2)
	require.NoError(t, err)
	assert.True(t, stop2, "second request over the cap should be rejected")
	assert.Equal(t, "rate_limited", rc2.RejectReason)

	// A different api key must not share key-1's bucket.
	rcOtherKey := newRateLimitTestContext(auth.Identity{TenantID: "t", UserID: "u", APIKeyID: "key-2"}, "gpt-5")
	stopOtherKey, err := s.Handle(rcOtherKey)
	require.NoError(t, err)
	assert.False(t, stopOtherKey, "a different api key must not be limited by key-1's cap")

	// The same key against a different REQUESTED model must not share the bucket —
	// quotas match what the client asked for (rc.Model), not a served/mapped model.
	rcOtherModel := newRateLimitTestContext(auth.Identity{TenantID: "t", UserID: "u", APIKeyID: "key-1"}, "claude-haiku-4-5")
	stopOtherModel, err := s.Handle(rcOtherModel)
	require.NoError(t, err)
	assert.False(t, stopOtherModel, "a different requested model must not be limited by the gpt-5 cap")
}

// A routing rule rewrites rc.Model to the resolved model before this stage runs; the
// cap on the model the client asked for must still apply, matching Reconcile.
func TestRatelimitStage_UsesRequestedModelAfterRouting(t *testing.T) {
	limiter := ratelimit.New(ratelimit.NewMemStore(), fakeLimitSource{[]ratelimit.Limit{
		{TenantID: "t", UserID: "u", APIKeyID: "key-1", Model: "gpt-5",
			Metric: ratelimit.MetricRequests, Period: ratelimit.PeriodMinute, Value: 1, Enabled: true},
	}})
	s := ratelimitStage{limiter: limiter}
	routed := func() *RequestContext {
		rc := newRateLimitTestContext(auth.Identity{TenantID: "t", UserID: "u", APIKeyID: "key-1"}, "gpt-5-mini")
		rc.Decision = routing.Decision{RequestedModel: "gpt-5", ResolvedModel: "gpt-5-mini"}
		return rc
	}

	stop, err := s.Handle(routed())
	require.NoError(t, err)
	assert.False(t, stop, "first routed request under the gpt-5 cap should pass")

	rc2 := routed()
	stop2, err := s.Handle(rc2)
	require.NoError(t, err)
	assert.True(t, stop2, "second routed request must hit the cap on the requested model")
	assert.Equal(t, "rate_limited", rc2.RejectReason)
}
