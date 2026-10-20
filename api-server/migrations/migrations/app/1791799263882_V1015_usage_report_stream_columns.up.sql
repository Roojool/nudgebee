-- #38972: the daily cost sync streams line items into cloud_account_usage_report
-- and derives billing resources and spends from these rows in SQL, instead of
-- holding the whole report in collector memory.
--   external_resource_id: the line's resource identity, computed by the collector
--                         (same value it matches cloud_resourses on).
--   resource_name:        provider-supplied display name (Azure), used for
--                         billing-sourced cloud_resourses.name.
--   line_no:              order of the line within one sync run; "first line wins"
--                         for a resource's tags and name.
-- Nullable ADD COLUMN without a default only touches the catalog: no table
-- rewrite, an ACCESS EXCLUSIVE lock held for milliseconds. Existing rows stay
-- NULL; they are replaced by the next sync of their month.
ALTER TABLE cloud_account_usage_report ADD COLUMN IF NOT EXISTS external_resource_id text;
ALTER TABLE cloud_account_usage_report ADD COLUMN IF NOT EXISTS resource_name text;
ALTER TABLE cloud_account_usage_report ADD COLUMN IF NOT EXISTS line_no bigint;
