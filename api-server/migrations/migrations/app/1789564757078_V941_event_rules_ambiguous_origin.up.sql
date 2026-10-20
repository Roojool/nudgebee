-- Epic #38553 (alert-rule writes, phase 1). Adds the columns the rule resolver
-- reads before it offers an edit or a disable-at-source:
--   ambiguous: a syncer collapsed more than one upstream rule into this row
--              (same alert name in two AWS regions, two PrometheusRules, ...),
--              so a write could land on the wrong rule.
--   origin:    who owns the rule definition (provider | nudgebee_ui |
--              nudgebee_cr | chart_cr | customer_cr). NULL = not yet classified.
-- Syncers start writing both in later PRs; readers treat NULL/false as unknown.
--
-- event_threshold_suggestions.previous_provider_config keeps the provider
-- settings an apply replaced (evaluation periods, datapoints, per-condition
-- thresholds) so a revert can restore them exactly.
--
-- Every statement is metadata-only (constant defaults), so the lock is brief.
ALTER TABLE event_rules ADD COLUMN IF NOT EXISTS ambiguous boolean NOT NULL DEFAULT false;
ALTER TABLE event_rules ADD COLUMN IF NOT EXISTS origin text;
ALTER TABLE event_threshold_suggestions ADD COLUMN IF NOT EXISTS previous_provider_config jsonb;
