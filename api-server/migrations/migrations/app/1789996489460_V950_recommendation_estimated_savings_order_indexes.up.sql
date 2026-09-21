CREATE INDEX IF NOT EXISTS idx_recommendation_account_status_savings
  ON recommendation (cloud_account_id, status, estimated_savings DESC NULLS LAST);

CREATE INDEX IF NOT EXISTS idx_recommendation_account_status_category_savings
  ON recommendation (cloud_account_id, status, category, estimated_savings DESC NULLS LAST);
