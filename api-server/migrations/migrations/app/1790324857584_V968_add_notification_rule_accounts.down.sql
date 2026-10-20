DROP TABLE IF EXISTS "public"."notification_rule_accounts";

ALTER TABLE "public"."notification_rules" DROP CONSTRAINT IF EXISTS "notification_rules_scope_mode_check";
ALTER TABLE "public"."notification_rules" DROP COLUMN IF EXISTS "scope_mode";
