-- recommendation sawtooths 0 -> ~10% dead on a ~2.8h cycle (scale_factor 0.1
-- on 1.37M live rows, ~1.18M dead tuples/day, ~0 HOT). Every scan of it slows
-- in the back half of each cycle: stale visibility-map pages force heap fetches
-- on Index Only Scans (154,730 of 341,519 rows on security_tab_counts).
-- Lower the trigger to ~2% and raise the cost limit so runs clear faster.
--
-- OPERATOR NOTE: this only changes WHEN autovacuum fires. Run once per env to
-- clear the current backlog (VACUUM can't run in a migration transaction):
--   VACUUM (ANALYZE) recommendation;
-- Then watch pg_stat_user_tables.n_dead_tup peaks stay under ~40k.

ALTER TABLE recommendation SET (
  autovacuum_vacuum_scale_factor  = 0.02,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_limit    = 2000,
  autovacuum_vacuum_cost_delay    = 2
);
