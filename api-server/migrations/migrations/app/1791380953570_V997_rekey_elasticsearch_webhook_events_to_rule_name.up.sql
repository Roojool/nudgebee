-- Move stored Elasticsearch/Kibana webhook events from the Kibana rule id to the
-- rule name as their aggregation_key.
--
-- The webhook parser stored the Kibana rule id (a UUID) as the event's
-- aggregation_key. That key is what the rule column shows and what matches an
-- event to its event_rules row — and that row is registered under the rule name,
-- so the two never met. The parser now stores the rule name.
--
-- A later delivery does not correct a stored event. A repeat of an alert that is
-- already stored updates its labels, priority and subject, never its
-- aggregation_key, and this source keeps one event row per alert instance. Without
-- this migration every alert that existed before the upgrade would show the rule id
-- for good.
--
-- Both values are on the event already: labels.rule_id is the Kibana id it was keyed
-- by, labels.alertname the rule name. A row is rewritten only while its key still
-- equals its rule_id and it carries a different, non-empty alertname, so both
-- statements are idempotent and leave name-keyed events alone.
--
-- event_log_analysis rows are read by (account, fingerprint, aggregation_key). They
-- move first, while the events they belong to still carry the old key.
--
-- Cost: both statements reach their rows through events filtered by source and
-- touch this source's events only — row locks on those rows, nothing wider. The
-- events update fires the table's updated_at trigger for the rows it rewrites.
--
-- Not rewritten: mutes, notification rules and workflow triggers that were pointed
-- at a Kibana rule id. They match on the same key and need pointing at the rule name.

UPDATE event_log_analysis a
   SET event_aggregation_key = e.labels->>'alertname'
  FROM events e
 WHERE e.source = 'elasticsearch_webhook'
   AND e.aggregation_key = e.labels->>'rule_id'
   AND COALESCE(e.labels->>'alertname', '') NOT IN ('', e.aggregation_key)
   AND a.cloud_account_id = e.cloud_account_id
   AND a.event_fingerprint = e.fingerprint
   AND a.event_aggregation_key = e.aggregation_key;

UPDATE events
   SET aggregation_key = labels->>'alertname'
 WHERE source = 'elasticsearch_webhook'
   AND aggregation_key = labels->>'rule_id'
   AND COALESCE(labels->>'alertname', '') NOT IN ('', aggregation_key);
