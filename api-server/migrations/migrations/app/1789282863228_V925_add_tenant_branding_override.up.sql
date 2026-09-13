-- Per-host branding overrides, editable at runtime.
--
-- Splits the branding artifact by CHURN RATE rather than by storage type.
-- A partner's asset kit (SVGs + the base theme.json) is onboarded once and
-- keeps living in the mounted ConfigMap, because assets need a filesystem the
-- Next.js static handler can serve and a deploy is an acceptable cost for a
-- once-ever event. What actually churns is presentation: palette, product
-- title, assistant name, fonts. Those move here so they can be changed without
-- an infra PR and a deploy (prod deploys are workflow_dispatch-only).
--
-- The row is an OVERRIDE LAYER, never the whole brand: app merges it ON TOP OF
-- the file resolved from TENANT_BRANDING_HOST_MAP / TENANT_BRANDING_FILE. It
-- tracks the theme.json contract field for field, so an operator does not have
-- to remember which half of the brand lives where. An
-- absent row, an unreachable database, or an empty `overrides` object are all
-- indistinguishable from today's behaviour — the mounted file remains the
-- floor. That property is load-bearing: branding used to consult
-- hasLicenseFeature('branding.custom'), which made a partner's white-label
-- depend on services-server being reachable at app boot, and a transient
-- failure silently reverted the UI to Nudgebee (see docs/ee/licensing-design.md
-- and migration V826, which dropped that feature). Nothing here may reintroduce
-- that failure mode.
--
-- Keyed by HOST, not tenant, and deliberately carries no tenant FK: branding
-- resolves BEFORE authentication (the signin page has no session and no tenant
-- context), so the only key available at resolution time is the request's
-- X-Forwarded-Host. `host` is stored already normalized by the writer —
-- lower-cased, port and trailing dot stripped, first entry of a comma-joined
-- list — matching normalizeBrandHost() in app/src/lib/serverBranding.ts. A key
-- normalized differently from the lookup would silently never match.
--
-- `previous_overrides` is a one-step undo. Moving a brand out of git costs the
-- `git revert` that the ConfigMap kit gets for free, and audit rows do not
-- close the gap on their own: audit.CreateAudit deliberately SKIPS super-admin
-- operations, which is exactly who edits partner branding. Keeping the prior
-- value on the row means undo works regardless of actor.
--
-- Lock window: CREATE TABLE on a new relation takes no lock on anything
-- existing. No backfill, no rewrite; runs in milliseconds.
CREATE TABLE IF NOT EXISTS tenant_branding_override (
    id                  uuid        NOT NULL DEFAULT gen_random_uuid(),
    host                text        NOT NULL,
    overrides           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    previous_overrides  jsonb,
    enabled             boolean     NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    created_by          uuid,
    updated_by          uuid,
    PRIMARY KEY (id),
    CONSTRAINT tenant_branding_override_host_uniq UNIQUE (host)
);

COMMENT ON TABLE tenant_branding_override IS
'Runtime-editable branding overrides, merged over the mounted brand kit. Host-keyed because branding resolves pre-auth. Never the sole source of a brand: an absent row leaves the mounted file in charge.';

COMMENT ON COLUMN tenant_branding_override.host IS
'Request hostname, pre-normalized by the writer to match normalizeBrandHost() in app/src/lib/serverBranding.ts (lower-case, no port, no trailing dot).';

COMMENT ON COLUMN tenant_branding_override.overrides IS
'theme.json-compatible fields, allow-listed by services-server: copy (title, assistantName), asset URLs (logoUrl, faviconUrl, the nubi/bee illustration set, carouselSlides), and styling (theme, colorTokens, fontRemap). Asset URLs are constrained to same-origin root-relative paths so a row cannot source imagery from another host; the files themselves still come from the mounted kit. isWhiteLabel and email are refused - the first is derived from which kit resolved, the second is read from the file by notifications-server.';

COMMENT ON COLUMN tenant_branding_override.previous_overrides IS
'The value of `overrides` immediately before the last write. Powers one-step undo, replacing the git revert lost by moving branding out of the infra repo.';

COMMENT ON COLUMN tenant_branding_override.enabled IS
'Operator kill-switch. False makes the row inert and the host falls back to its mounted kit, without losing the stored override.';
