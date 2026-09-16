-- Records the tenant's default LLM provider as an integration_config_values row
-- (name = default_llm_provider, value = 'true') on that LLM integration. The
-- tenant has at most one; it is linked and flagged default on every cloud
-- account created afterwards. The per-account default_llm_provider flag on
-- integrations_cloud_accounts is unchanged and still decides resolution.
--
-- Backfill: a tenant whose flagged LLM account links all point at one enabled
-- integration already has an unambiguous default, so mark it. A tenant with
-- several distinct flagged integrations is left unmarked until an admin picks
-- one. Data-only: inserts at most one row per tenant, no lock of note.

INSERT INTO integration_config_values (id, integration_id, name, value, is_encrypted, created_at, updated_at)
SELECT
    gen_random_uuid(),
    i.id,
    'default_llm_provider',
    'true',
    false,
    now(),
    now()
FROM integrations i
WHERE i."type" = 'llm'
  AND i.status = 'enabled'
  AND EXISTS (
      SELECT 1
        FROM integrations_cloud_accounts ia
       WHERE ia.integration_id = i.id
         AND ia.default_llm_provider
  )
  AND (
      SELECT count(DISTINCT i2.id)
        FROM integrations i2
        JOIN integrations_cloud_accounts ia2 ON ia2.integration_id = i2.id
       WHERE i2.tenant_id = i.tenant_id
         AND i2."type" = 'llm'
         AND i2.status = 'enabled'
         AND ia2.default_llm_provider
  ) = 1
ON CONFLICT (integration_id, name) DO NOTHING;
