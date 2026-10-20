-- API key policy: key kind, a usable expiry, and per-key/per-model quota dimensions.

-- kind is platform (app API), gateway (AI Gateway) or all (both). Existing rows take the
-- default, so every key that predates the split keeps the all-access reach it was minted
-- with; the split binds only what is created from here on. Only provider='token' rows are
-- ever read as API keys, so the value on SSO rows is inert.
ALTER TABLE public.user_auths
  ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'all';

-- expires_at was `timestamp` (no zone) while the gateway compares it to now() (timestamptz),
-- so expiry fired at the wrong hour. Guarded because the USING clause is not idempotent:
-- against an already-timestamptz column it would strip the zone and re-read the value in
-- the session zone, shifting every expiry.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'user_auths'
       AND column_name = 'expires_at' AND data_type = 'timestamp without time zone'
  ) THEN
    ALTER TABLE public.user_auths
      ALTER COLUMN expires_at TYPE timestamptz USING expires_at AT TIME ZONE 'UTC';
  END IF;
END $$;

-- Quota rows gain the key and model dimensions. '' keeps the table's existing meaning: any.
ALTER TABLE public.llm_gateway_rate_limits
  ADD COLUMN IF NOT EXISTS api_key_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS model      text NOT NULL DEFAULT '';

-- Usage rows carry the stable key id. token_id holds the token NAME, which is neither
-- stable across renames nor unique across users, so it cannot drive per-key spend.
ALTER TABLE public.llm_gateway_usage
  ADD COLUMN IF NOT EXISTS api_key_id text NOT NULL DEFAULT '';

-- (user, name, provider) was unique, which made one name mean one key. With three kinds a
-- user may reasonably have "laptop" as both a platform and a gateway key.
ALTER TABLE public.user_auths DROP CONSTRAINT IF EXISTS user_auths_user_name_provider_key;
CREATE UNIQUE INDEX IF NOT EXISTS user_auths_user_name_provider_kind_key
  ON public.user_auths ("user", name, provider, kind);

-- Serves the gateway's per-key limit lookup. Small table, plain build is fine.
CREATE INDEX IF NOT EXISTS idx_llm_gw_ratelimits_key
  ON public.llm_gateway_rate_limits (tenant_id, user_id, api_key_id) WHERE enabled;

-- Serves per-key usage/spend reads. llm_gateway_usage is large: build this out of band
-- with CREATE INDEX CONCURRENTLY IF NOT EXISTS on each live database BEFORE merging, so
-- this statement is a no-op there. On-prem installs pay the plain build.
CREATE INDEX IF NOT EXISTS idx_llm_gw_usage_api_key
  ON public.llm_gateway_usage (tenant_id, api_key_id, created_at);
