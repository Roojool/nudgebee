-- Not reversed: after the upgrade new events are stored under the rule name too,
-- and nothing marks which rows this migration rewrote. Moving name-keyed events back
-- to the rule id would undo more than the up migration did.
SELECT 1;
