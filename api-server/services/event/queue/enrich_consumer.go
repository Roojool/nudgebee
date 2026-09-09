package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/config"
	"nudgebee/services/event"
	eventtypes "nudgebee/services/event/types"
	"nudgebee/services/security"
)

const (
	enrichmentDedupNamespace = "event_enrichment_dedup"
	enrichmentDedupTTL       = 1 * time.Hour
	// enrichmentRetryBackoff spaces out self-driven retries. Republishing
	// immediately would burn the whole attempt budget inside a few milliseconds
	// of a transient database blip and then give up for no reason.
	enrichmentRetryBackoff = 15 * time.Second
)

var enrichmentDedupOnce sync.Once

// Indirection seams so processEventEnrichment is unit-testable without a live
// DB / MQ. Overridden in tests.
var (
	enrichEventFn             = event.EnrichEvent
	publishEventPostProcessFn = publishPostProcessForEnrichedEvent
	republishEnrichmentFn     = republishEnrichment
)

func init() {
	// Do not dial RabbitMQ during tests.
	if testing.Testing() {
		return
	}
	err := common.MqConsume(
		config.Config.RabbitMqEventEnrichExchange,
		config.Config.RabbitMqEventEnrichQueue,
		config.Config.RabbitMqEventEnrichQueue,
		config.Config.RabbitMqEventEnrichConcurrency,
		processEventEnrichment,
		// Unlike every other queue here, losing one of these on a broker restart
		// would leave the event both unenriched and never post-processed.
		common.MqConsumeWithDurableQueue(),
	)
	if err != nil {
		slog.Error("event_enrich_queue: failed to start consumer", "error", err)
	}
}

// processEventEnrichment runs the playbook for one already-persisted event and
// then hands it on to the post-process pipeline.
//
// It returns nil on every path, deliberately. MqConsume nacks on nil and
// NackRequeues on any error, with no dead-letter queue and no delivery limit, so
// a returned error is an unbounded redelivery loop. Retries here are self-driven
// by republishing with an incremented attempt count, which a broker requeue could
// not express — it redelivers the identical bytes.
//
// The other invariant: every exit that abandons enrichment still publishes to
// event_post_process. Enrichment is best-effort; triage, notification and the LLM
// summary are not, and an event that never reaches them is invisible to the user.
func processEventEnrichment(msgCtx context.Context, data []byte) error {
	var msg eventtypes.EventEnrichmentMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Error("event_enrich_queue: failed to unmarshal message, dropping", "error", err, "data", string(data))
		return nil
	}

	// Both of these are load-bearing: the event id addresses the row, and the
	// tenant id scopes every query the enrichment makes. A message missing either
	// can never succeed, so drop it rather than burn the retry budget on it.
	// account_id is deliberately not required — it only feeds the dedup key,
	// logging and metrics, so an empty one still processes correctly.
	eventID := strings.TrimSpace(msg.EventID)
	tenantID := strings.TrimSpace(msg.TenantID)
	if eventID == "" || tenantID == "" {
		slog.Error("event_enrich_queue: message missing event_id or tenant_id, dropping",
			"event_id", eventID, "tenant_id", tenantID)
		return nil
	}

	logger := slog.Default().With("event_id", eventID, "account_id", msg.AccountID, "attempt", msg.Attempt)

	// A panic here would otherwise kill the process: go-rabbitmq runs handlers on
	// its own goroutines, which the recover() around consumer.Run in MqConsume
	// never sees. common.processMessageAndDetermineAction now recovers too, but
	// that one drops the message — recovering here lets the event still reach
	// triage without its evidence.
	defer func() {
		if r := recover(); r != nil {
			logger.Error("event_enrich_queue: PANIC during enrichment, failing open",
				"panic", r, "stack", string(debug.Stack()))
			publishEventPostProcessFn(msgCtx, msg, logger)
		}
	}()

	// Idempotency: hand an event to post-processing at most once. The marker is
	// set only after a successful publish, so a transient failure cannot suppress
	// a later legitimate delivery.
	enrichmentDedupOnce.Do(func() {
		common.CacheCreateNamespace(enrichmentDedupNamespace,
			common.CacheNamespaceWithExpiration(enrichmentDedupTTL))
	})
	dedupKey := msg.AccountID + ":" + eventID
	if _, found := common.CacheGet(enrichmentDedupNamespace, dedupKey); found {
		logger.Info("event_enrich_queue: already enriched, skipping duplicate")
		return nil
	}

	// Carry the budget on the context as well as passing it explicitly. The
	// between-actions check in ExecutePlaybook cannot interrupt an action already
	// in flight, but anything context-aware further down — the relay call behind
	// the incident-time anchor, for one — is cancelled the moment the budget runs
	// out. The write path is unaffected: applyEnrichment executes without a
	// context, and the post-process publish uses msgCtx, so an expired budget can
	// never cost us the evidence we just collected.
	// A budget of zero or less means unbounded, matching how ExecutePlaybook reads
	// a zero deadline. Without this an operator setting the budget to 0 to mean
	// "no limit" would get the exact inverse — every playbook aborting before its
	// first action, silently, with enrichment permanently empty.
	var (
		enrichCtx = msgCtx
		cancel    = context.CancelFunc(func() {})
		deadline  time.Time
	)
	if budget := config.Config.EventEnrichBudgetSeconds; budget > 0 {
		deadline = time.Now().Add(time.Duration(budget) * time.Second)
		enrichCtx, cancel = context.WithDeadline(msgCtx, deadline)
	}
	defer cancel()

	sc := security.NewRequestContext(
		enrichCtx,
		security.NewSecurityContextForTenantAdmin(tenantID),
		logger, nil, nil,
	)

	start := time.Now()
	outcome, err := enrichEventFn(sc, eventID, deadline)
	duration := time.Since(start).Seconds()
	common.MetricsEventEnrichmentDuration(msgCtx, duration, "", msg.TenantID, msg.AccountID, outcome)

	if err != nil {
		// Could not read or write the row. Retry a bounded number of times, then
		// fail open so the event is not stranded without triage.
		if msg.Attempt < config.Config.RabbitMqEventEnrichMaxAttempts-1 {
			logger.Warn("event_enrich_queue: enrichment failed, will retry", "error", err, "duration_s", duration)
			republishEnrichmentFn(msgCtx, msg, logger)
			return nil
		}
		logger.Error("event_enrich_queue: enrichment failed permanently, failing open to post-process",
			"error", err, "duration_s", duration, "attempts", msg.Attempt+1)
	} else {
		logger.Info("event_enrich_queue: enrichment complete", "outcome", outcome, "duration_s", duration)
	}

	publishEventPostProcessFn(msgCtx, msg, logger)
	if err := common.CacheSet(enrichmentDedupNamespace, dedupKey, []byte("1"),
		common.CacheSetWithExpiration(enrichmentDedupTTL)); err != nil {
		logger.Warn("event_enrich_queue: failed to set dedup marker", "error", err)
	}
	return nil
}

// publishPostProcessForEnrichedEvent hands the enriched event to the existing
// post-process pipeline. Published rather than called directly so triage and the
// LLM step keep running on their own queue's workers instead of occupying an
// enrichment slot.
func publishPostProcessForEnrichedEvent(msgCtx context.Context, msg eventtypes.EventEnrichmentMessage, logger *slog.Logger) {
	err := common.MqPublish(
		config.Config.RabbitMqEventPostProcessExchange,
		config.Config.RabbitMqEventPostProcessQueue,
		map[string]any{"event_id": msg.EventID},
		common.MqPublishWithExpiration(1*time.Hour),
		common.MqPublishWithBackgroundRetry(),
		common.MqPublishWithContext(msgCtx),
	)
	if err != nil {
		logger.Error("event_enrich_queue: failed to publish post-process", "error", err)
	}
}

// republishEnrichment re-queues the job with the attempt count incremented, after
// a backoff. The sleep occupies one consumer slot; with concurrency > 1 that is
// cheaper than the alternatives (a delayed exchange we do not have, or a broker
// requeue that cannot carry the counter).
func republishEnrichment(msgCtx context.Context, msg eventtypes.EventEnrichmentMessage, logger *slog.Logger) {
	select {
	case <-msgCtx.Done():
		logger.Warn("event_enrich_queue: context cancelled before retry, dropping")
		return
	case <-time.After(enrichmentRetryBackoff):
	}

	msg.Attempt++
	err := common.MqPublish(
		config.Config.RabbitMqEventEnrichExchange,
		config.Config.RabbitMqEventEnrichQueue,
		msg,
		common.MqPublishWithPersistentDelivery(),
		common.MqPublishWithBackgroundRetry(),
		common.MqPublishWithContext(msgCtx),
	)
	if err != nil {
		logger.Error("event_enrich_queue: failed to republish for retry", "error", err)
	}
}
