CREATE INDEX IF NOT EXISTS idx_cloud_resourses_active_account_service
  ON cloud_resourses (account, service_name) INCLUDE (name, type, region)
  WHERE is_active;
