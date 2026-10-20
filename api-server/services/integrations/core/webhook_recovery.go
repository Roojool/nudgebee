package core

import (
	"fmt"
	"time"

	"nudgebee/services/config"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"
)

// The age cutoffs are computed by the database, not by this process:
// event_created_at is `timestamp without time zone` holding UTC wall clock (the
// insert in ValidateAndStoreWebhook runs on a UTC pod), so comparing it against
// a Go-side local time would shift the window by the pod's offset.
//
// Stuck-webhook recovery. event_incoming_webhooks is the durable record of every
// delivery, so a row that never left 'pending' is a webhook the queue lost —
// publish discarded by the broker, consumer down, or the message aged out of the
// queue. A row stuck in 'processing' is one whose pod died mid-flight. Both are
// invisible to the broker and only the table knows about them.
const (
	// Old enough that the normal async path (publish → consume → process, well
	// under a second) has certainly had its chance.
	stuckWebhookPendingAge = 10 * time.Minute
	// Generous: processing runs LLM-backed enrichment for some integrations.
	stuckWebhookProcessingAge = 30 * time.Minute
	// Bounds one run. Recovery is per-row and re-runs on the next tick.
	stuckWebhookBatchSize = 200
	// Recorded on rows too old to re-drive, so the Activity Log says why.
	stuckWebhookExpiredError = "not recovered: older than stuck_webhook_recovery_max_age_minutes"
)

// stuckWebhookMaxAge is the oldest delivery recovery will re-drive. Below the
// pending age (10m) nothing is re-driven; 0 turns re-driving off entirely.
func stuckWebhookMaxAge() time.Duration {
	return time.Duration(config.Config.StuckWebhookRecoveryMaxAgeMinutes) * time.Minute
}

// RecoverStuckWebhooks re-drives webhook deliveries that the async path never
// completed. Called by the "Stuck Webhook Recovery" cron job.
//
// Safe to run concurrently with the queue consumer and with itself: every row is
// claimed by ProcessStoredWebhook's atomic pending → processing transition, so a
// row picked up twice is processed once.
func RecoverStuckWebhooks(sc *security.RequestContext) error {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return fmt.Errorf("integrations: failed to get database manager: %w", err)
	}

	// Rows whose processor died mid-flight go back to pending so the claim below
	// can pick them up. Without this they are stranded forever — nothing else
	// ever moves a row out of 'processing'.
	res, err := dbms.Db.Exec(
		`UPDATE event_incoming_webhooks SET processing_status = 'pending'
		 WHERE processing_status = 'processing'
		   AND event_created_at < (now() AT TIME ZONE 'utc') - make_interval(secs => $1)`,
		stuckWebhookProcessingAge.Seconds())
	if err != nil {
		return fmt.Errorf("integrations: failed to reset stalled webhooks: %w", err)
	}
	if reset, rowsErr := res.RowsAffected(); rowsErr == nil && reset > 0 {
		sc.GetLogger().Warn("webhook recovery: reset webhooks stalled in processing", "count", reset)
	}

	// A stale alert replayed now is analysed against today's data, so it is
	// skipped instead; that also lets the retention cleanup delete it.
	res, err = dbms.Db.Exec(
		`UPDATE event_incoming_webhooks SET processing_status = 'skipped', processing_error = $2
		 WHERE processing_status = 'pending'
		   AND event_created_at < (now() AT TIME ZONE 'utc') - make_interval(secs => $1)`,
		stuckWebhookMaxAge().Seconds(), stuckWebhookExpiredError)
	if err != nil {
		return fmt.Errorf("integrations: failed to expire stale webhooks: %w", err)
	}
	if expired, rowsErr := res.RowsAffected(); rowsErr == nil && expired > 0 {
		sc.GetLogger().Warn("webhook recovery: skipped webhooks too old to re-drive", "count", expired)
	}

	var rowIDs []string
	err = dbms.Db.Select(&rowIDs,
		`SELECT id::text FROM event_incoming_webhooks
		 WHERE processing_status = 'pending'
		   AND event_created_at < (now() AT TIME ZONE 'utc') - make_interval(secs => $1)
		 ORDER BY event_created_at LIMIT $2`,
		stuckWebhookPendingAge.Seconds(), stuckWebhookBatchSize)
	if err != nil {
		return fmt.Errorf("integrations: failed to list stuck webhooks: %w", err)
	}
	if len(rowIDs) == 0 {
		return nil
	}

	sc.GetLogger().Warn("webhook recovery: found webhooks the queue never delivered", "count", len(rowIDs))

	var recovered, failed int
	for _, rowID := range rowIDs {
		if err := ProcessStoredWebhook(sc, rowID); err != nil {
			failed++
			sc.GetLogger().Error("webhook recovery: failed to process stuck webhook", "webhook_row_id", rowID, "error", err)
			continue
		}
		recovered++
	}
	sc.GetLogger().Info("webhook recovery: run complete", "recovered", recovered, "failed", failed)
	return nil
}
