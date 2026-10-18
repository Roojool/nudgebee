CREATE INDEX IF NOT EXISTS idx_cloud_resource_metrics_tenantaccount_podfqdn
  ON cloud_resource_metrics (tenant_id, cloud_account_id, (tags ->> 'pod_fqdn'), "timestamp")
  WHERE (tags ->> 'pod_fqdn') IS NOT NULL;
