-- Remediation plans look an event's subject up in the inventory by (account, name): the workload's
-- containers, limits and rollout revision are what make a generated command runnable (#38594).
--
-- The closest existing index is (account, type, name), and that lookup has no type equality to offer
-- it — an event's subject_type is "deployment" where inventory stores "Deployment", and for cloud
-- resources the vocabularies do not overlap at all. So Postgres scanned the whole account range:
-- 2.1M rows, 25k buffers, 4.9s per generation on dev.
--
-- Lock window: this is a plain CREATE INDEX, which takes a SHARE lock and blocks writes to
-- cloud_resourses for the duration (~40s for 3.6M rows on dev). It is written to be a no-op where
-- the index was already built out-of-band with CREATE INDEX CONCURRENTLY, which is how it is applied
-- to live databases before this merges; CONCURRENTLY cannot run inside a migration's transaction.
CREATE INDEX IF NOT EXISTS idx_cloud_resourses_account_name
    ON cloud_resourses (account, name);
