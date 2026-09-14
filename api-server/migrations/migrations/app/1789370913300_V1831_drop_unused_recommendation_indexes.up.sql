-- Drop three legacy single-column indexes on `recommendation`, superseded by
-- composite indexes added later that already lead with the same column, so
-- the planner never needs the narrower one:
--   * recommendation_tenant      (tenant_id)   -- 65 scans/2mo, 46 MB.
--     Superseded by recommendation_tenant_resource, tenant_status_category_rule_name,
--     and idx_recommendation_tenant_account_status (all lead with tenant_id).
--   * recommendation_resourceId  (resource_id) -- 3,318 scans/2mo, 56 MB.
--     Superseded by idx_recommendation_resource_account (resource_id, cloud_account_id).
--   * recommendation_rulename    (rule_name)   -- 342 scans/2mo, 60 MB.
--     Superseded by recommendation_rule_name_cloud_account_id_account_object_id_cat
--     (UNIQUE, leads with rule_name).
--
-- Confirmed via pg_stat_user_indexes (idx_scan far below their superseding
-- composite indexes) and a repo-wide grep: none of the three are unique
-- constraints, none are referenced by name (ON CONFLICT / ON CONSTRAINT)
-- anywhere in api-server or app, and no query in the codebase filters
-- recommendation by tenant_id, resource_id, or rule_name alone without also
-- scoping by cloud_account_id/status/category — exactly what the composite
-- indexes above already cover.
--
-- Plain DROP INDEX (NOT concurrent) — same reasoning as V752: this takes a
-- brief ACCESS EXCLUSIVE lock but only unlinks catalog entries (no scan), so
-- it stays fast even on a large table.

DROP INDEX IF EXISTS recommendation_tenant;
DROP INDEX IF EXISTS "recommendation_resourceId";
DROP INDEX IF EXISTS recommendation_rulename;
