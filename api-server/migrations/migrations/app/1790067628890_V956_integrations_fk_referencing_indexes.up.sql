CREATE INDEX IF NOT EXISTS idx_event_incoming_webhooks_integration_id
  ON event_incoming_webhooks (integration_id);

CREATE INDEX IF NOT EXISTS idx_integration_user_accounts_integration_id
  ON integration_user_accounts (integration_id);

CREATE INDEX IF NOT EXISTS idx_tickets_integration_id
  ON tickets (integration_id);
