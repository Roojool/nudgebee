-- Reverts the prompt-versioning tables to provider-scoping. Data-losing by
-- construction (a free-form model string has no mapping back to a fixed
-- provider enum); safe only because these tables carry no production rows
-- (see up.sql).

ALTER TABLE llm_prompt_configuration DROP CONSTRAINT IF EXISTS unique_llm_prompt_config;
DROP INDEX IF EXISTS idx_llm_prompt_config_lookup;
ALTER TABLE llm_prompt_configuration DROP COLUMN IF EXISTS model;
ALTER TABLE llm_prompt_configuration ADD COLUMN IF NOT EXISTS provider VARCHAR(50) NOT NULL DEFAULT 'default'
    CHECK (provider IN ('default', 'bedrock', 'azure', 'openai', 'googleai', 'anthropic', 'vertexai', 'huggingface', 'sagemaker'));
ALTER TABLE llm_prompt_configuration ADD CONSTRAINT unique_llm_prompt_config UNIQUE (prompt_name, category, provider, account_id);
CREATE INDEX IF NOT EXISTS idx_llm_prompt_config_lookup ON llm_prompt_configuration(prompt_name, category, provider, account_id) WHERE enabled = TRUE;

ALTER TABLE llm_prompt_experiments DROP COLUMN IF EXISTS models;
ALTER TABLE llm_prompt_experiments ADD COLUMN IF NOT EXISTS providers TEXT[];

ALTER TABLE llm_prompt_usage_metrics DROP COLUMN IF EXISTS model;
ALTER TABLE llm_prompt_usage_metrics ADD COLUMN IF NOT EXISTS provider VARCHAR(50) NOT NULL DEFAULT 'default';

ALTER TABLE llm_prompt_config_audit DROP COLUMN IF EXISTS model;
ALTER TABLE llm_prompt_config_audit ADD COLUMN IF NOT EXISTS provider VARCHAR(50);
