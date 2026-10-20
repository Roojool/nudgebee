CREATE INDEX IF NOT EXISTS idx_cloud_resource_metrics_tenantaccountmetric
    ON cloud_resource_metrics (tenant_id, cloud_account_id, metric, "timestamp");
