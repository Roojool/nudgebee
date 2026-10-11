-- Fixes #38860: a followup run is an in-process goroutine that only polls
-- llm-server, so a services-server pod replaced mid-run drops the goroutine and
-- strands the row in 'addressing' while llm-server's run carries on. Recovery
-- used to be the 6h cron after a 45-minute lease measured from
-- last_pr_check_at — which every mid-run signal also advances.
--
--   run_token            identifies the current run. Minted when a run is
--                        claimed, cleared when it finalizes; finalize only
--                        applies if the token still matches, so a poller that
--                        lost the row (or a second poller on the same run)
--                        cannot apply an outcome twice.
--   run_conversation_id  the llm-server conversation the run is polling, saved
--                        as soon as it is submitted. A later pod re-attaches to
--                        it instead of dispatching a duplicate agent.
--   run_heartbeat_at     stamped every ~60s while a poller is alive. Staleness
--                        of this column, not of last_pr_check_at, is what marks
--                        a run dead.
--
-- All three are nullable with no default: ADD COLUMN is metadata-only, no table
-- rewrite. Rows written by pods that predate this change leave them NULL and
-- keep the old 45-minute last_pr_check_at lease, so a rolling deploy of this
-- change does not reclaim their in-flight runs. No index: they are only read on
-- a row already located by id, or by the small 'addressing' state set.
ALTER TABLE "public"."pr_followup"
    ADD COLUMN IF NOT EXISTS "run_token" uuid,
    ADD COLUMN IF NOT EXISTS "run_conversation_id" text,
    ADD COLUMN IF NOT EXISTS "run_heartbeat_at" timestamptz;
