ALTER TABLE vulnerabilities RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_analyze_scale_factor,
  autovacuum_vacuum_cost_limit,
  autovacuum_vacuum_cost_delay
);
