-- Platform API key scopes: which modules a key may call.
-- Nullable column with no default: a catalog-only change, no table rewrite, lock window is
-- the brief ACCESS EXCLUSIVE to update the catalog.

-- NULL means the key has its owner's full access, which is every key that exists today.
-- Otherwise a list of `<module>:<Read|Write|Execute>` keys from the app's permission
-- catalog; the app gateway requires every call to match one of them.
ALTER TABLE public.user_auths
  ADD COLUMN IF NOT EXISTS scopes text[];
