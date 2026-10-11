ALTER TABLE public.user_auths ALTER COLUMN accessed_at DROP DEFAULT;

-- Backfill existing never-used API tokens whose accessed_at was defaulted to created_at
UPDATE public.user_auths
SET accessed_at = NULL
WHERE provider = 'token'
  AND accessed_at = created_at;
