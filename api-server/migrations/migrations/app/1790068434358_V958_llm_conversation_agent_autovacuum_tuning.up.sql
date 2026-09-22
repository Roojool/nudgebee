ALTER TABLE llm_conversation_agent SET (
  autovacuum_vacuum_scale_factor  = 0.02,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_limit    = 2000,
  autovacuum_vacuum_cost_delay    = 2
);
