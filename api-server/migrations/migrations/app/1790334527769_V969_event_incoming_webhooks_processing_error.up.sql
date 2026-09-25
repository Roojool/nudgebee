-- Persist why a webhook delivery failed to process. Before this, a failed
-- delivery only carried processing_status = 'failed' and the reason lived in
-- the service log alone.
ALTER TABLE event_incoming_webhooks
  ADD COLUMN IF NOT EXISTS processing_error TEXT;
