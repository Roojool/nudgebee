-- get_security_severity_groupings joins vulnerabilities once per matched CVE
-- finding (~81k Index Scan + heap fetch on vulnerabilities_pkey, just to read
-- vuln_id for count(DISTINCT)). An INCLUDE covering index makes it index-only.
-- Dev: that node's buffers 325k -> 280k, whole query 410k -> 366k. vuln_id is
-- set at ingest and never updated, so it never makes an UPDATE non-HOT.
--
-- OPERATOR NOTE (migrations-lint.yaml rejects CONCURRENTLY): pre-apply per env
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_vulnerabilities_id_incl_vuln_id
--     ON vulnerabilities (id) INCLUDE (vuln_id);
-- The statement below is then a no-op; where not pre-applied it holds a SHARE
-- lock for the build (~seconds at 229MB), blocking writes but not reads.

CREATE INDEX IF NOT EXISTS idx_vulnerabilities_id_incl_vuln_id
  ON vulnerabilities (id) INCLUDE (vuln_id);

ANALYZE vulnerabilities;
