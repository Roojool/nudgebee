-- Remove the ms_teams_webhook type. Only drops the lookup rows when no data
-- references them, so a rollback on an env where webhooks are in use is a no-op
-- rather than a FK error.
DELETE FROM notification_platform_types
WHERE value = 'ms_teams_webhook'
  AND NOT EXISTS (
    SELECT 1 FROM notification_rule_mappings WHERE platform = 'ms_teams_webhook'
  );

DELETE FROM integration_types
WHERE name = 'ms_teams_webhook'
  AND NOT EXISTS (
    SELECT 1 FROM integrations WHERE type = 'ms_teams_webhook'
  );
