-- Activity Log query support for event_incoming_webhooks (per-integration event
-- log + total/processed counts on Admin -> Integrations -> <webhook>). Both
-- indexes lead on integration_id, the query's fixed filter:
--   * (integration_id, event_created_at DESC) — serves the newest-first event
--     list without a sort, and the total-events count.
--   * (integration_id, processing_status)     — serves the processed-events count.
--
-- LOCK WINDOW: a plain CREATE INDEX takes a SHARE lock that blocks writes
-- (webhook ingestion) for the build. event_incoming_webhooks is ~2.1M rows, so
-- the build is a few seconds. Per CLAUDE.md (CONCURRENTLY cannot run in a
-- migration transaction and is rejected by migrations-lint), build these
-- out-of-band with CREATE INDEX CONCURRENTLY IF NOT EXISTS on each live database
-- BEFORE merging — the statements below are then a no-op there. On-prem installs
-- run this file via the Helm post-upgrade hook and take the brief lock, which is
-- acceptable for a one-time build. IF NOT EXISTS keeps it idempotent either way.
CREATE INDEX IF NOT EXISTS idx_event_incoming_webhooks_integration_created
  ON event_incoming_webhooks (integration_id, event_created_at DESC);

CREATE INDEX IF NOT EXISTS idx_event_incoming_webhooks_integration_processing
  ON event_incoming_webhooks (integration_id, processing_status);
