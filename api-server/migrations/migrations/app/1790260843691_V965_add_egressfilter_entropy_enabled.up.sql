ALTER TABLE IF EXISTS public.llm_egressfilter_tenant_config
    ADD COLUMN IF NOT EXISTS entropy_enabled BOOLEAN NOT NULL DEFAULT false;
