-- Notification rules are evaluated in an admin-set order: the first matching
-- rule decides, and a rule with continue_matching also lets later rules match.
-- The backfill reproduces the previous "any rule wins" outcome: suppress rules
-- first, then snoozes (a deliver rule with a window) latest expiry first, so any
-- snooze still active precedes every expired one, then every other rule; all but
-- the suppress rules continue matching. Ties go by creation.
--
-- Lock window: ACCESS EXCLUSIVE on notification_rules for the whole file (small
-- table, one UPDATE). Expected well under a second at current table sizes.

ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "position" integer NULL;
ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "continue_matching" boolean NULL;

UPDATE "public"."notification_rules" AS r
SET "position" = ordered.rank * 1000,
    "continue_matching" = ordered.grp <> 1
FROM (
    SELECT
        "id",
        CASE
            WHEN COALESCE("is_suppressed", false) THEN 1
            WHEN "expires_at" IS NOT NULL THEN 2
            ELSE 3
        END AS grp,
        row_number() OVER (
            PARTITION BY "tenant_id", "source"
            ORDER BY
                CASE
                    WHEN COALESCE("is_suppressed", false) THEN 1
                    WHEN "expires_at" IS NOT NULL THEN 2
                    ELSE 3
                END,
                "expires_at" DESC NULLS LAST,
                "created_at",
                "id"
        ) AS rank
    FROM "public"."notification_rules"
) AS ordered
WHERE r."id" = ordered."id"
  AND r."position" IS NULL;
