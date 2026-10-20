INSERT INTO "public"."notification_source_type"("value") VALUES ('auto_pilot') ON CONFLICT DO NOTHING;

DELETE FROM "public"."notification_source_type"
WHERE "value" = 'automation'
  AND NOT EXISTS (SELECT 1 FROM "public"."notification_rules" WHERE "source" = 'automation');

ALTER TABLE "public"."notification_rules" DROP COLUMN IF EXISTS "event_types";
