CREATE INDEX IF NOT EXISTS cloud_resource_metrics_tenantaccount ON cloud_resource_metrics (tenant_id, cloud_account_id);
CREATE INDEX IF NOT EXISTS cloud_resource_metrics_metric ON cloud_resource_metrics (metric);
CREATE INDEX IF NOT EXISTS cloud_resource_metrics_resourceid ON cloud_resource_metrics (cloud_resource_id);
