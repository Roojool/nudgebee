CREATE INDEX IF NOT EXISTS idx_recommendation_severity_weight
    ON recommendation (
        cloud_account_id,
        status,
        (CASE
            WHEN severity = 'Critical' THEN 10
            WHEN severity = 'High'     THEN 8
            WHEN severity = 'Medium'   THEN 5
            WHEN severity = 'Low'      THEN 2
            WHEN severity = 'Info'     THEN 1
            ELSE 0
        END) DESC
    );
