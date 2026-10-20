-- TOAST tables do not inherit their parent's autovacuum reloptions. The tuned
-- autovacuum_vacuum_scale_factor=0.02 on events/cloud_resourses/recommendation/
-- spends never reaches pg_toast_*, which keeps running at the 0.2 default. Those
-- options only apply when spelled with the `toast.` prefix.
--
-- Measured on dev 2026-09-20: events' TOAST had gone unvacuumed since 09-03 and
-- sat at 62.6% bloat -- 36 GB of live chunks inside a 95 GB relation. Driver is
-- events.evidences (jsonb), which averages ~69 kB and reaches 2.5 MB per row
-- (one sampled row = 440 kB = 221 TOAST chunks); a jsonb update rewrites every
-- chunk of the value, so dead chunks accumulate fast. At scale_factor 0.2 the
-- trigger sits near 3.4M dead chunks; 0.02 moves it to ~350k.
-- llm_conversation_{tool_calls,agent} carry multi-GB TOAST under the same
-- default and had never been autovacuumed at all (autovacuum_count = 0).
--
-- This is invisible to normal monitoring: ANALYZE truncates width statistics
-- above 1 kB, so pg_stats reports events.evidences as avg_width = 43 bytes.
--
-- OPERATOR NOTE: this only changes WHEN autovacuum fires; it does not clear the
-- existing backlog (VACUUM cannot run inside a migration transaction). Unlike
-- the CONCURRENTLY case this is not a hard requirement -- autovacuum clears the
-- backlog by itself once the lower threshold applies. To do it immediately:
--   VACUUM (ANALYZE) events;
--   VACUUM (ANALYZE) llm_conversation_tool_calls;
--   VACUUM (ANALYZE) llm_conversation_agent;
-- On dev that cleared 95 GB of events TOAST in 75 s with no exclusive lock.
--
-- Returning the freed bytes to the OS would need VACUUM FULL / pg_repack and is
-- deliberately out of scope: the space is reusable, so the table grows back into
-- it rather than extending the disk, and a Cloud SQL disk never shrinks anyway.

ALTER TABLE IF EXISTS events SET (
  toast.autovacuum_vacuum_scale_factor = 0.02,
  toast.autovacuum_vacuum_threshold    = 1000,
  toast.autovacuum_vacuum_cost_limit   = 2000,
  toast.autovacuum_vacuum_cost_delay   = 2
);

ALTER TABLE IF EXISTS llm_conversation_tool_calls SET (
  toast.autovacuum_vacuum_scale_factor = 0.02,
  toast.autovacuum_vacuum_threshold    = 1000,
  toast.autovacuum_vacuum_cost_limit   = 2000,
  toast.autovacuum_vacuum_cost_delay   = 2
);

ALTER TABLE IF EXISTS llm_conversation_agent SET (
  toast.autovacuum_vacuum_scale_factor = 0.02,
  toast.autovacuum_vacuum_threshold    = 1000,
  toast.autovacuum_vacuum_cost_limit   = 2000,
  toast.autovacuum_vacuum_cost_delay   = 2
);
