-- Pod health as a column, so it can be counted and filtered.
--
-- k8s_pods.status holds the pod PHASE, which is "Running" for a pod in
-- CrashLoopBackOff and "Pending" for one in ImagePullBackOff. The pods table
-- already shows the real reason by computing it from meta.status_info per
-- displayed row, but the cluster Summary counts and the Status filter read
-- `status`, so every broken pod is filed under a healthy-sounding bucket and
-- cannot be filtered for (#36593).
--
-- Those two read whole sets rather than a page of 25 rows, and the jsonb has to
-- be detoasted per pod: measured on dev, 10s for one 87k-pod account and over
-- two minutes to filter a single account's 333k historical pods. A column the
-- collector writes costs nothing to read.
--
-- Expected lock window: milliseconds. ADD COLUMN on a nullable column with no
-- default is a catalog-only change, so the ACCESS EXCLUSIVE lock it takes is
-- held only long enough to write the catalog row.
--
-- The backfill is deliberately a SEPARATE migration (V947). Atlas applies each
-- file in one transaction (--tx-mode file), so putting the UPDATE here would
-- hold that ACCESS EXCLUSIVE lock -- which blocks every read AND write of
-- k8s_pods, not just the rows being written -- for as long as the scan takes.
-- On prod that table is 31GB with 291k live pods and the scan has to read meta
-- for each one; measured on dev, the equivalent scan is ~10s, and prod is ~3x
-- the live rows on colder data. Split, the lock here is trivial and the
-- backfill takes only row locks.
ALTER TABLE k8s_pods ADD COLUMN IF NOT EXISTS container_status text;

COMMENT ON COLUMN k8s_pods.container_status IS
  'kubectl STATUS reason (CrashLoopBackOff, ImagePullBackOff, OOMKilled, ...) when a container is broken, NULL when the pod is healthy. Written by the k8s collector; readers fall back to status (the phase) when NULL.';
