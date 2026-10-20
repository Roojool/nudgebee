CREATE INDEX IF NOT EXISTS idx_cloud_resourses_account_billing_active
    ON cloud_resourses (account, external_resource_id)
    WHERE status <> 'Deleted' AND meta->>'nb_source' = 'billing';
