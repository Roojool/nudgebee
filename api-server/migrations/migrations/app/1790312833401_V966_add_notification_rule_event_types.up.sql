-- Adds the per-rule event type selection used by automation rules and
-- registers the automation notification source. Lock window: ADD COLUMN
-- with no default is a catalog-only change; the enum inserts touch one
-- row each.
ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "event_types" json NULL;

INSERT INTO "public"."notification_source_type"("value") VALUES ('automation') ON CONFLICT DO NOTHING;

DELETE FROM "public"."notification_source_type"
WHERE "value" = 'auto_pilot'
  AND NOT EXISTS (SELECT 1 FROM "public"."notification_rules" WHERE "source" = 'auto_pilot');
