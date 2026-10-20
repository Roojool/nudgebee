CREATE INDEX IF NOT EXISTS idx_cloud_resource_metrics_tenantaccount_workloadfqdn
    ON cloud_resource_metrics (tenant_id, cloud_account_id, (tags ->> 'workload_fqdn'), "timestamp");
