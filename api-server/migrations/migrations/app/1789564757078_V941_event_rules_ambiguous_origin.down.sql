ALTER TABLE event_threshold_suggestions DROP COLUMN IF EXISTS previous_provider_config;
ALTER TABLE event_rules DROP COLUMN IF EXISTS origin;
ALTER TABLE event_rules DROP COLUMN IF EXISTS ambiguous;
