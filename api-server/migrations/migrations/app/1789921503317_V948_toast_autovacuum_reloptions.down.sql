-- Reverts V948: drop the per-TOAST autovacuum overrides so the TOAST tables fall
-- back to the server defaults (scale_factor 0.2). None of the three had any
-- toast.* reloptions before V948, so RESET restores the prior state exactly.
ALTER TABLE IF EXISTS events RESET (
  toast.autovacuum_vacuum_scale_factor,
  toast.autovacuum_vacuum_threshold,
  toast.autovacuum_vacuum_cost_limit,
  toast.autovacuum_vacuum_cost_delay
);

ALTER TABLE IF EXISTS llm_conversation_tool_calls RESET (
  toast.autovacuum_vacuum_scale_factor,
  toast.autovacuum_vacuum_threshold,
  toast.autovacuum_vacuum_cost_limit,
  toast.autovacuum_vacuum_cost_delay
);

ALTER TABLE IF EXISTS llm_conversation_agent RESET (
  toast.autovacuum_vacuum_scale_factor,
  toast.autovacuum_vacuum_threshold,
  toast.autovacuum_vacuum_cost_limit,
  toast.autovacuum_vacuum_cost_delay
);
