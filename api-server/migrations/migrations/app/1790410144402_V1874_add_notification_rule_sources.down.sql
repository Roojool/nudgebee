-- The expires_at and is_active changes are not reverted: before this migration
-- neither value changed how those rules behaved.
ALTER TABLE "public"."notification_rules" DROP CONSTRAINT IF EXISTS "notification_rules_source_mode_check";
ALTER TABLE "public"."notification_rules" DROP COLUMN IF EXISTS "sources";
ALTER TABLE "public"."notification_rules" DROP COLUMN IF EXISTS "source_mode";
