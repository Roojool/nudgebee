CREATE INDEX IF NOT EXISTS idx_k8s_pods_account_workload_type
  ON k8s_pods (cloud_account_id, tenant_id, workload_type);
