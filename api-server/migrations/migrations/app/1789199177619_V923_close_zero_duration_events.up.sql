-- Close the alerts that arrived already ended and were stored as firing anyway
-- (issue #38238).
--
-- A cloud webhook that reports an instant -- "ECS Task State Change", "GKE
-- Resource Upgrade Started", "Kubernetes Secret Modified", a relayed CloudWatch
-- ALARM -- says so by sending ends_at equal to starts_at. Ingestion stored those
-- as FIRING, which asserts an ongoing condition the producer never claimed, and
-- nothing ever closes them: there is no resolve to come for a moment that has
-- already passed. They simply accumulate.
--
-- Measured before this ran:
--   * Rackspace: 18,357 `ECS Task State Change` alerts FIRING at a median age of
--     52 days -- 69% of everything stuck open on that estate -- plus ~850 more
--     across Azure VM health, GCP upgrade/secret notifications and RDS deadlock
--     alerts. ~19,200 rows in total.
--   * Production: 917 rows across AWS_EventBridge (343), AWS_CloudWatch_Alarm
--     (276), pagerduty_webhook (177), GCP_Metric_Alert (96) and
--     Azure_Monitor_Alert (25).
--
-- The companion code change makes InsertEvent store these CLOSED on arrival, so
-- this is the one-off catch-up for rows already written.
--
-- SCOPE, and why each clause is here:
--
--   ends_at IS NOT NULL AND ends_at <= starts_at
--       The producer's own statement that the event has no duration. An interval
--       where ends_at is AFTER starts_at is a condition that ran and stopped; its
--       producer owns that lifecycle and sends its own resolve, so it is left
--       alone. This is the whole safety argument: we are not judging whether
--       something recovered, only honouring an end time already supplied.
--
--   status = 'FIRING'
--       Only the rows that misrepresent themselves. RESOLVED rows are already
--       terminal -- on production those are cost-anomaly notifications whose
--       titles literally read "Anomaly Resolved" -- and closing them would churn
--       state for no gain. Also makes this idempotent.
--
-- Deliberately not scoped to a source or aggregation_key: the defect is the
-- shape of the data, not its origin, and naming today's producers would leave
-- the next one to rediscover this.
--
-- Deliberately does NOT touch nb_status, matching the other closers and the
-- code path this backfills.
--
-- ends_at is left exactly as the producer sent it. Unlike V921 there is nothing
-- to infer: the end time is already on the row, which is the entire premise.
--
-- LOCK WINDOW: one UPDATE plus the matching event_history inserts in a single
-- transaction. ~19k rows on the largest estate, far smaller elsewhere. The
-- predicate reads only scalar columns and never touches `evidences`, so none of
-- the TOASTed payloads are read. Expected to run in seconds.

WITH target AS (
    SELECT e.id,
           e.status AS old_status,
           e.tenant,
           e.cloud_account_id
      FROM events e
     WHERE e.status = 'FIRING'
       AND e.ends_at IS NOT NULL
       AND e.starts_at IS NOT NULL
       AND e.ends_at <= e.starts_at
       -- event_history.tenant_id / cloud_account_id are NOT NULL; a row missing
       -- either cannot get a history entry and would abort the whole migration.
       AND e.tenant IS NOT NULL
       AND e.cloud_account_id IS NOT NULL
),
closed AS (
    UPDATE events e
       SET status     = 'CLOSED',
           updated_at = now() AT TIME ZONE 'utc'
      FROM target t
     WHERE e.id = t.id
    RETURNING e.id, t.old_status, t.tenant, t.cloud_account_id
)
INSERT INTO event_history (
    id, event_id, tenant_id, cloud_account_id,
    change_type, old_value, new_value, change_reason, metadata
)
SELECT
    -- Deterministic id, so a re-run inserts nothing new. md5 is 32 hex
    -- characters, a valid uuid literal, and needs no extension.
    md5(c.id::text || '|status|' || c.old_status || '|CLOSED')::uuid,
    c.id,
    c.tenant,
    c.cloud_account_id,
    'status',
    to_jsonb(c.old_status),
    '"CLOSED"'::jsonb,
    'zero_duration_backfill',
    jsonb_build_object(
        'method',    'migration',
        'migration', 'V923',
        'trigger',   'ends_at_at_or_before_starts_at',
        'issue',     '38238'
    )
  FROM closed c
    ON CONFLICT (id) DO NOTHING;
