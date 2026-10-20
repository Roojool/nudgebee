-- What happens to a notification when no rule decides it, per tenant and source.
-- No row keeps the built-in behaviour: every platform's default channel.
--   deliver_defaults: each platform's default channel (the built-in behaviour)
--   deliver_channels: only the channels in `mappings` (same shape as rule mappings)
--   drop:             nothing is sent
-- Kept out of notification_rules on purpose: every reader of that table would
-- treat a "drop" row as a tenant-wide suppress rule.
--
-- Lock window: creates a new table; the tenant foreign key takes SHARE ROW
-- EXCLUSIVE on tenant until commit. Expected well under a second.

CREATE TABLE IF NOT EXISTS "public"."notification_default_policies" (
  "tenant_id" uuid NOT NULL REFERENCES "public"."tenant"("id") ON UPDATE CASCADE ON DELETE CASCADE,
  "source" text NOT NULL,
  "action" text NOT NULL,
  "mappings" jsonb NOT NULL DEFAULT '[]'::jsonb,
  "updated_by" uuid NULL,
  "updated_at" timestamp NOT NULL DEFAULT now(),
  PRIMARY KEY ("tenant_id", "source"),
  CONSTRAINT "notification_default_policies_action_check"
    CHECK ("action" IN ('deliver_defaults', 'deliver_channels', 'drop'))
);
