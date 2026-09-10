-- Reverts V920: restore the prior per-table autovacuum settings and drop the
-- two that V920 introduced.
ALTER TABLE recommendation SET (
  autovacuum_vacuum_scale_factor  = 0.1,
  autovacuum_analyze_scale_factor = 0.05
);
ALTER TABLE recommendation RESET (
  autovacuum_vacuum_cost_limit,
  autovacuum_vacuum_cost_delay
);
