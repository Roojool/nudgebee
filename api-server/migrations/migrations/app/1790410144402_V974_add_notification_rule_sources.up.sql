-- The notification types a rule covers. source_mode 'all' covers every type,
-- including ones added later; 'list' covers the types in sources. NULL is a row
-- written before this column existed (or by an older pod): it covers [source].
-- source stays the mirror older readers use: the first listed type.
--
-- Two data changes keep behaviour the same once the matcher reads them:
--   1. A suppressed rule used to suppress whatever its expires_at said; an
--      expiry now ends the suppression, so the expiry on existing suppressed
--      rules is cleared and they stay suppressed.
--   2. Optimize rules scoped to accounts have matched nothing since optimize
--      became tenant-wide; optimize can be scoped to accounts again, so they are
--      switched off rather than starting to match.
--
-- Lock window: the ADD COLUMNs are catalog-only; the two UPDATEs touch a few
-- rows of notification_rules (row locks for milliseconds).

ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "source_mode" text NULL;
ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "sources" text[] NULL;

ALTER TABLE "public"."notification_rules" DROP CONSTRAINT IF EXISTS "notification_rules_source_mode_check";
ALTER TABLE "public"."notification_rules"
  ADD CONSTRAINT "notification_rules_source_mode_check" CHECK ("source_mode" IS NULL OR "source_mode" IN ('all', 'list'));

UPDATE "public"."notification_rules"
SET "expires_at" = NULL
WHERE "is_suppressed" IS TRUE AND "expires_at" IS NOT NULL;

UPDATE "public"."notification_rules"
SET "is_active" = FALSE
WHERE "source" = 'optimize' AND "scope_mode" = 'accounts' AND "is_active" IS NOT FALSE;
