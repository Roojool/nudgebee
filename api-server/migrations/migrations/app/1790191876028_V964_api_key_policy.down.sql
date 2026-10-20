DROP INDEX IF EXISTS public.idx_llm_gw_usage_api_key;
DROP INDEX IF EXISTS public.idx_llm_gw_ratelimits_key;

ALTER TABLE public.llm_gateway_usage DROP COLUMN IF EXISTS api_key_id;

ALTER TABLE public.llm_gateway_rate_limits
  DROP COLUMN IF EXISTS model,
  DROP COLUMN IF EXISTS api_key_id;

ALTER TABLE public.user_auths
  ALTER COLUMN expires_at TYPE timestamp USING expires_at AT TIME ZONE 'UTC';

DROP INDEX IF EXISTS public.user_auths_user_name_provider_kind_key;

-- Restoring the pre-split UNIQUE(user, name, provider) requires that no two rows share
-- that triple. The up migration deliberately allows one name per kind, so rename the
-- newer collisions instead of deleting them: a rollback must not destroy credentials.
UPDATE public.user_auths ua
   SET name = left(ua.name, 200) || '-' || ua.kind
  FROM (
    SELECT id, row_number() OVER (PARTITION BY "user", name, provider ORDER BY created_at, id) AS rn
      FROM public.user_auths
  ) d
 WHERE d.id = ua.id AND d.rn > 1;

ALTER TABLE public.user_auths DROP COLUMN IF EXISTS kind;

ALTER TABLE public.user_auths
  ADD CONSTRAINT user_auths_user_name_provider_key UNIQUE ("user", name, provider);
