-- Converts the prompt-versioning tables from provider-scoping to model-scoping.
--
-- The provider dimension (bedrock/azure/openai/googleai/anthropic/vertexai/...)
-- never distinguished individual models, and the admin API/UI that would write
-- these tables was never wired up (handlePromptsApis is not registered in
-- api.ConfigureRoutes) -- these tables carry no production rows to migrate.
-- This is a straight column swap, not a data migration.

-- llm_prompt_configuration: provider -> model
--
-- The original V658 constraint was a plain UNIQUE(..., account_id), and
-- account_id is nullable ("NULL = global default"). Postgres treats NULLs as
-- distinct in a standard unique constraint, so two global rows for the same
-- (prompt_name, category, model) could both insert and ON CONFLICT would never
-- fire between them -- the same bug class V649 already fixed for
-- `recommendation`. No production rows exist to have hit this yet (see file
-- header), so fixing it here rather than carrying it forward unchanged.
ALTER TABLE llm_prompt_configuration DROP CONSTRAINT IF EXISTS unique_llm_prompt_config;
DROP INDEX IF EXISTS idx_llm_prompt_config_lookup;
ALTER TABLE llm_prompt_configuration DROP COLUMN IF EXISTS provider;
ALTER TABLE llm_prompt_configuration ADD COLUMN IF NOT EXISTS model VARCHAR(255) NOT NULL DEFAULT 'default';
ALTER TABLE llm_prompt_configuration ADD CONSTRAINT unique_llm_prompt_config UNIQUE NULLS NOT DISTINCT (prompt_name, category, model, account_id);
CREATE INDEX IF NOT EXISTS idx_llm_prompt_config_lookup ON llm_prompt_configuration(prompt_name, category, model, account_id) WHERE enabled = TRUE;
-- idx_llm_prompt_config_account keys off account_id only; unaffected.

-- llm_prompt_experiments: providers[] -> models[] (NULL/empty still means "all models")
ALTER TABLE llm_prompt_experiments DROP COLUMN IF EXISTS providers;
ALTER TABLE llm_prompt_experiments ADD COLUMN IF NOT EXISTS models TEXT[];

-- llm_prompt_usage_metrics: provider -> model
ALTER TABLE llm_prompt_usage_metrics DROP COLUMN IF EXISTS provider;
ALTER TABLE llm_prompt_usage_metrics ADD COLUMN IF NOT EXISTS model VARCHAR(255) NOT NULL DEFAULT 'default';

-- llm_prompt_config_audit: provider -> model (nullable, matches prior provider column)
ALTER TABLE llm_prompt_config_audit DROP COLUMN IF EXISTS provider;
ALTER TABLE llm_prompt_config_audit ADD COLUMN IF NOT EXISTS model VARCHAR(255);
