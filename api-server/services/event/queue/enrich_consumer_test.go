package queue

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/config"
	eventtypes "nudgebee/services/event/types"
	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
)

// swapEnrichSeams replaces the package-level seams for one test and restores them
// afterwards, returning counters for what the consumer did.
type enrichSpy struct {
	enrichCalls     int
	postProcessed   int
	republished     int
	lastDeadlineSet bool
}

func swapEnrichSeams(t *testing.T, enrich func(*security.RequestContext, string, time.Time) (string, error)) *enrichSpy {
	t.Helper()
	spy := &enrichSpy{}

	origEnrich, origPublish, origRepublish := enrichEventFn, publishEventPostProcessFn, republishEnrichmentFn
	t.Cleanup(func() {
		enrichEventFn, publishEventPostProcessFn, republishEnrichmentFn = origEnrich, origPublish, origRepublish
	})

	enrichEventFn = func(sc *security.RequestContext, eventID string, deadline time.Time) (string, error) {
		spy.enrichCalls++
		spy.lastDeadlineSet = !deadline.IsZero()
		return enrich(sc, eventID, deadline)
	}
	publishEventPostProcessFn = func(_ context.Context, _ eventtypes.EventEnrichmentMessage, _ *slog.Logger) {
		spy.postProcessed++
	}
	republishEnrichmentFn = func(_ context.Context, _ eventtypes.EventEnrichmentMessage, _ *slog.Logger) {
		spy.republished++
	}
	return spy
}

func enrichMessage(t *testing.T, msg eventtypes.EventEnrichmentMessage) []byte {
	t.Helper()
	data, err := json.Marshal(msg)
	assert.NoError(t, err)
	return data
}

// clearEnrichDedup drops any marker left by an earlier test in this package.
func clearEnrichDedup(t *testing.T, msg eventtypes.EventEnrichmentMessage) {
	t.Helper()
	enrichmentDedupOnce.Do(func() {
		common.CacheCreateNamespace(enrichmentDedupNamespace,
			common.CacheNamespaceWithExpiration(enrichmentDedupTTL))
	})
	_ = common.CacheDelete(enrichmentDedupNamespace, msg.AccountID+":"+msg.EventID)
}

// The queue has no dead-letter exchange and MqConsume requeues forever on a
// returned error, so any error escaping this processor is an infinite redelivery
// loop. Every path must return nil.
func TestProcessEventEnrichment_AlwaysReturnsNil(t *testing.T) {
	tests := []struct {
		name              string
		data              []byte
		enrich            func(*security.RequestContext, string, time.Time) (string, error)
		wantPostProcessed int
		wantRepublished   int
	}{
		{
			name:              "malformed json is dropped",
			data:              []byte("{not json"),
			enrich:            func(*security.RequestContext, string, time.Time) (string, error) { return "", nil },
			wantPostProcessed: 0,
		},
		{
			name:              "missing event id is dropped",
			data:              enrichMessage(t, eventtypes.EventEnrichmentMessage{AccountID: "a"}),
			enrich:            func(*security.RequestContext, string, time.Time) (string, error) { return "", nil },
			wantPostProcessed: 0,
		},
		{
			name: "blank event id is dropped",
			data: enrichMessage(t, eventtypes.EventEnrichmentMessage{EventID: "   ", AccountID: "a"}),
			enrich: func(*security.RequestContext, string, time.Time) (string, error) {
				t.Fatal("enrichment must not run for a blank event id")
				return "", nil
			},
			wantPostProcessed: 0,
		},
		{
			// The tenant scopes every query enrichment makes, so a message without
			// one can never succeed — drop it instead of spending the retry budget.
			name: "missing tenant id is dropped",
			data: enrichMessage(t, eventtypes.EventEnrichmentMessage{EventID: "evt-no-tenant", AccountID: "a"}),
			enrich: func(*security.RequestContext, string, time.Time) (string, error) {
				t.Fatal("enrichment must not run without a tenant id")
				return "", nil
			},
			wantPostProcessed: 0,
		},
		{
			// account_id only feeds the dedup key, logging and metrics, so its
			// absence must NOT discard an otherwise-processable message.
			name: "missing account id still processes",
			data: enrichMessage(t, eventtypes.EventEnrichmentMessage{EventID: "evt-no-account", TenantID: "t"}),
			enrich: func(*security.RequestContext, string, time.Time) (string, error) {
				return "success", nil
			},
			wantPostProcessed: 1,
		},
		{
			name: "successful enrichment hands off to post-process",
			data: enrichMessage(t, eventtypes.EventEnrichmentMessage{EventID: "evt-ok", AccountID: "a", TenantID: "t"}),
			enrich: func(*security.RequestContext, string, time.Time) (string, error) {
				return "success", nil
			},
			wantPostProcessed: 1,
		},
		{
			name: "playbook produced nothing but the event still moves on",
			data: enrichMessage(t, eventtypes.EventEnrichmentMessage{EventID: "evt-partial", AccountID: "a", TenantID: "t"}),
			enrich: func(*security.RequestContext, string, time.Time) (string, error) {
				return "partial", nil
			},
			wantPostProcessed: 1,
		},
		{
			name: "panic is recovered and fails open",
			data: enrichMessage(t, eventtypes.EventEnrichmentMessage{EventID: "evt-panic", AccountID: "a", TenantID: "t"}),
			enrich: func(*security.RequestContext, string, time.Time) (string, error) {
				panic("playbook exploded")
			},
			wantPostProcessed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg eventtypes.EventEnrichmentMessage
			_ = json.Unmarshal(tt.data, &msg)
			clearEnrichDedup(t, msg)

			spy := swapEnrichSeams(t, tt.enrich)

			err := processEventEnrichment(context.Background(), tt.data)

			assert.NoError(t, err, "a returned error would requeue forever: no DLQ, no delivery limit")
			assert.Equal(t, tt.wantPostProcessed, spy.postProcessed,
				"an event that never reaches post-process is never triaged or notified")
			assert.Equal(t, tt.wantRepublished, spy.republished)
		})
	}
}

func TestProcessEventEnrichment_RetriesThenFailsOpen(t *testing.T) {
	config.Config.RabbitMqEventEnrichMaxAttempts = 3

	t.Run("retries below the attempt cap", func(t *testing.T) {
		msg := eventtypes.EventEnrichmentMessage{EventID: "evt-retry", AccountID: "a", TenantID: "t", Attempt: 0}
		clearEnrichDedup(t, msg)
		spy := swapEnrichSeams(t, func(*security.RequestContext, string, time.Time) (string, error) {
			return "error", errors.New("database is down")
		})

		err := processEventEnrichment(context.Background(), enrichMessage(t, msg))

		assert.NoError(t, err)
		assert.Equal(t, 1, spy.republished)
		assert.Equal(t, 0, spy.postProcessed, "a retryable failure should not consume the one post-process handoff")
	})

	t.Run("fails open once attempts are exhausted", func(t *testing.T) {
		msg := eventtypes.EventEnrichmentMessage{EventID: "evt-exhausted", AccountID: "a", TenantID: "t", Attempt: 2}
		clearEnrichDedup(t, msg)
		spy := swapEnrichSeams(t, func(*security.RequestContext, string, time.Time) (string, error) {
			return "error", errors.New("database is still down")
		})

		err := processEventEnrichment(context.Background(), enrichMessage(t, msg))

		assert.NoError(t, err)
		assert.Equal(t, 0, spy.republished)
		assert.Equal(t, 1, spy.postProcessed,
			"an event whose enrichment can never succeed must still reach triage, unenriched")
	})
}

// A redelivery after a successful run must not push the event through triage,
// the LLM summary and notification a second time.
func TestProcessEventEnrichment_DedupSuppressesSecondHandoff(t *testing.T) {
	msg := eventtypes.EventEnrichmentMessage{EventID: "evt-dedup", AccountID: "acct-dedup", TenantID: "t"}
	clearEnrichDedup(t, msg)

	spy := swapEnrichSeams(t, func(*security.RequestContext, string, time.Time) (string, error) {
		return "success", nil
	})

	assert.NoError(t, processEventEnrichment(context.Background(), enrichMessage(t, msg)))
	assert.NoError(t, processEventEnrichment(context.Background(), enrichMessage(t, msg)))

	assert.Equal(t, 1, spy.postProcessed, "the second delivery must not re-trigger post-processing")
	assert.Equal(t, 1, spy.enrichCalls, "the second delivery must not re-run the playbook")
}

// The budget is the only thing keeping one event from outliving the broker's
// 60-minute consumer_timeout and being redelivered forever.
func TestProcessEventEnrichment_PassesADeadline(t *testing.T) {
	config.Config.EventEnrichBudgetSeconds = 900
	msg := eventtypes.EventEnrichmentMessage{EventID: "evt-deadline", AccountID: "a", TenantID: "t"}
	clearEnrichDedup(t, msg)

	spy := swapEnrichSeams(t, func(_ *security.RequestContext, _ string, deadline time.Time) (string, error) {
		assert.WithinDuration(t, time.Now().Add(900*time.Second), deadline, 5*time.Second)
		return "success", nil
	})

	assert.NoError(t, processEventEnrichment(context.Background(), enrichMessage(t, msg)))
	assert.True(t, spy.lastDeadlineSet, "enrichment must always be bounded")
}

// Setting the budget to 0 is the natural way to ask for "no limit". It must not
// produce the inverse — a deadline already in the past, aborting every playbook
// before its first action and leaving enrichment silently empty.
func TestProcessEventEnrichment_ZeroBudgetMeansUnbounded(t *testing.T) {
	original := config.Config.EventEnrichBudgetSeconds
	t.Cleanup(func() { config.Config.EventEnrichBudgetSeconds = original })

	for _, budget := range []int{0, -1} {
		config.Config.EventEnrichBudgetSeconds = budget
		msg := eventtypes.EventEnrichmentMessage{EventID: "evt-budget", AccountID: "a", TenantID: "t"}
		clearEnrichDedup(t, msg)

		spy := swapEnrichSeams(t, func(sc *security.RequestContext, _ string, deadline time.Time) (string, error) {
			assert.True(t, deadline.IsZero(), "budget %d must mean unbounded, not already expired", budget)
			assert.NoError(t, sc.GetContext().Err(), "the context must not arrive already cancelled")
			return "success", nil
		})

		assert.NoError(t, processEventEnrichment(context.Background(), enrichMessage(t, msg)))
		assert.Equal(t, 1, spy.enrichCalls)
	}
}
