-- Reverse the backfill: restore every alert this migration closed to the status
-- it held before, and remove the history rows that recorded the change.
--
-- Exact, not approximate: the up migration wrote each alert's previous status
-- into event_history.old_value, so the restore reads the record rather than
-- assuming everything was FIRING. ends_at was never modified, so there is
-- nothing to put back. Alerts closed by anything else carry a different
-- change_reason and are untouched.

WITH restored AS (
    SELECT h.event_id, h.old_value #>> '{}' AS old_status
      FROM event_history h
     WHERE h.change_reason = 'zero_duration_backfill'
)
UPDATE events e
   SET status     = r.old_status,
       updated_at = now() AT TIME ZONE 'utc'
  FROM restored r
 WHERE e.id = r.event_id
   AND e.status = 'CLOSED';

DELETE FROM event_history WHERE change_reason = 'zero_duration_backfill';
