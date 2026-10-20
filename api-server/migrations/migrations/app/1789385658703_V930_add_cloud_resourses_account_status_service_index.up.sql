CREATE INDEX IF NOT EXISTS idx_cloud_resourses_account_status_service
    ON cloud_resourses (account, status, service_name);
