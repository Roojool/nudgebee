-- Remove the google_chat_webhook type. Only drops the lookup rows when no data
-- references them, so a rollback on an env where webhooks are in use is a no-op
-- rather than a FK error.
DELETE FROM notification_platform_types
WHERE value = 'google_chat_webhook'
  AND NOT EXISTS (
    SELECT 1 FROM notification_rule_mappings WHERE platform = 'google_chat_webhook'
  );

DELETE FROM integration_types
WHERE name = 'google_chat_webhook'
  AND NOT EXISTS (
    SELECT 1 FROM integrations WHERE type = 'google_chat_webhook'
  );
