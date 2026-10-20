-- Findings a notification rule leaves out. A list of groups; a finding is left
-- out when any group matches, and a group matches when every key it has
-- (account_ids, namespaces, workloads, aggregation_keys) lists the finding's
-- value. NULL or an empty list leaves nothing out.
--
-- Lock window: ADD COLUMN with no default is a catalog-only change; ACCESS
-- EXCLUSIVE on notification_rules for milliseconds.

ALTER TABLE "public"."notification_rules" ADD COLUMN IF NOT EXISTS "exceptions" jsonb NULL;
