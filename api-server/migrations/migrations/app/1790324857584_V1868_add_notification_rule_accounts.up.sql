-- A notification rule's accounts move to notification_rule_accounts; scope_mode
-- says whether the rule covers every account or only the listed ones.
-- notification_rules.account_id stays as a compatibility mirror for older
-- clients and is no longer used to decide scope.
--
-- Lock window: ACCESS EXCLUSIVE on notification_rules for the whole file (small
-- table); the new table's foreign keys take SHARE ROW EXCLUSIVE on
-- cloud_accounts and tenant until commit, which briefly blocks writes to those
-- tables. Expected well under a second at current table sizes.

ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "scope_mode" text NULL;

UPDATE "public"."notification_rules"
SET "scope_mode" = CASE WHEN "account_id" IS NULL THEN 'all' ELSE 'accounts' END
WHERE "scope_mode" IS NULL;

ALTER TABLE "public"."notification_rules" ALTER COLUMN "scope_mode" SET NOT NULL;

ALTER TABLE "public"."notification_rules" DROP CONSTRAINT IF EXISTS "notification_rules_scope_mode_check";
ALTER TABLE "public"."notification_rules"
  ADD CONSTRAINT "notification_rules_scope_mode_check" CHECK ("scope_mode" IN ('all', 'accounts'));

CREATE TABLE IF NOT EXISTS "public"."notification_rule_accounts" (
  "rule_id" uuid NOT NULL REFERENCES "public"."notification_rules"("id") ON UPDATE CASCADE ON DELETE CASCADE,
  "account_id" uuid NOT NULL REFERENCES "public"."cloud_accounts"("id") ON UPDATE CASCADE ON DELETE CASCADE,
  "tenant_id" uuid NOT NULL REFERENCES "public"."tenant"("id") ON UPDATE CASCADE ON DELETE CASCADE,
  PRIMARY KEY ("rule_id", "account_id")
);

CREATE INDEX IF NOT EXISTS "notification_rule_accounts_account_id_idx" ON "public"."notification_rule_accounts" ("account_id");
CREATE INDEX IF NOT EXISTS "notification_rule_accounts_tenant_id_idx" ON "public"."notification_rule_accounts" ("tenant_id");

INSERT INTO "public"."notification_rule_accounts" ("rule_id", "account_id", "tenant_id")
SELECT "id", "account_id", "tenant_id"
FROM "public"."notification_rules"
WHERE "account_id" IS NOT NULL
ON CONFLICT DO NOTHING;
