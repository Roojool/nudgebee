# API Key Policy: type split, expiry, and per-key limits

Date: 2026-09-21
Status: Design approved, not implemented
Parent epic: #33932 (NB AI Gateway). Expiry portion: #34151.

## Problem

An NB Personal Access Token today is a single credential that:

- never expires,
- authenticates both the platform API (`/api/auth/token` exchange -> session JWT -> `/rpc/*`)
  and the AI Gateway (`/v1/*` raw bearer),
- carries no per-key spend or usage limits (quotas exist only at tenant and user scope).

A leaked key is therefore valid forever, grants both app data access and LLM spend, and
cannot be capped independently of its owner.

## Goals

1. Two distinct credential types with no crossover: platform keys and AI Gateway keys.
2. Optional expiry on new keys, enforced on both authentication paths.
3. Quotas that can be scoped to a single key and to a single model, on AI Gateway keys only.
4. A key owner can see their own key's usage and remaining quota.
5. No existing integration breaks on deploy day.

## Non-goals

- Scoped tokens in the permission-subset sense (a key carrying fewer roles than its owner).
  Explicitly deferred.
- Admin visibility over other users' keys. The list stays owner-scoped.
- Model allowlists as a 403 gate. Per-model restriction is expressed as a quota (429).
- Fixing `users_create_token` granting create to read-only roles. Belongs to #35441.

## Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Three kinds: `platform` works only on the exchange path, `gateway` only on `/v1/*`, `all` on both. The two single-purpose kinds are what the tabs mint; `all` must be chosen | A leaked gateway key cannot read app data; a leaked platform key cannot burn LLM spend. `all` exists because some callers genuinely need both, but nothing widens a key's reach by accident |
| D2 | Existing keys become `kind='all'`, and an omitted `kind` mints `all` | A PAT has always opened both doors, so migrating to anything narrower breaks live callers — 16 keys and ~44k gateway calls on dev alone. Enforcement applies to what is minted from here on, not retroactively. Narrowing is the feature flag's job |
| D3 | Key and model become quota dimensions, not a separate allowlist concept | Reuses the existing limiter, counter store and config table. One enforcement concept, one UI |
| D4 | Owner sets expiry and limits on their own keys, bounded by admin-configured tenant/user ceilings | Keeps admins out of per-key management (per the visibility decision) while capping blast radius |
| D5 | Expiry is owner's choice including "never" | Accepted trade-off: lowest friction for long-lived CI integrations |
| D6 | Limits match the client-requested model, not the mapped served model | A cap on "gpt-5" must keep meaning what the user typed even if an admin remaps it. Matches the existing metering choice (`proxy/pipeline.go:46`) |
| D7 | Every new restriction returns 429, never 403 | All of them are quotas. `Exceeded.Scope` gains `key` and `key+model` so the message names the cap that tripped |
| D8 | A platform key carries expiry and nothing else. `limits[]` on a platform create is rejected with 400, not silently ignored | Quotas only mean something on metered LLM traffic. A silently-dropped cap would read as protection that was never applied. `all` accepts limits, which bind its gateway calls only — the create dialog says so, since a cap that covers half a key's traffic must not read as covering all of it |

## Data model

`user_auths` (existing; `provider_type='credentials'`, `provider='token'`):

| Column | Change | Notes |
|---|---|---|
| `kind` | add, `text NOT NULL DEFAULT 'all'` | `platform` / `gateway` / `all`. The default backfills every existing row with no code, and matches what an omitted `kind` mints |
| `expires_at` | `ALTER TYPE timestamptz USING expires_at AT TIME ZONE 'UTC'` | Exists and is nullable (V6 dropped NOT NULL) but is `timestamp` without zone while the gateway compares it to `now()` (timestamptz). Expiry would otherwise fire at the wrong hour |
| `id` | unchanged | Already a uuid; becomes the stable key identifier that limits and usage join on |

Token prefixes: `sk-nb-gw-` for gateway, `sk-nb-` for platform and `all`. Both hashes cover
the whole prefixed string, as today. The prefix is a scanner and readability aid, not an
auth control — no code branches on it after minting; `kind` in the row is what decides.

`UNIQUE("user","name","provider")` gains `kind`, so the same name may exist once per type.
That makes name-based delete ambiguous, so `users_delete_token` moves to id (name accepted
for back-compat).

`llm_gateway_rate_limits` gains `api_key_id text NOT NULL DEFAULT ''` and
`model text NOT NULL DEFAULT ''`, following the table's existing `'' = any` convention:

| tenant | user | api_key | model | metric | period | value | meaning |
|---|---|---|---|---|---|---|---|
| t1 | '' | '' | '' | cost | month | 500 | tenant ceiling (exists today) |
| t1 | u9 | '' | '' | cost | month | 50 | per-user ceiling (exists today) |
| t1 | u9 | k3 | '' | cost | month | 20 | new: this key |
| t1 | u9 | k3 | gpt-5 | tokens | day | 100000 | new: this key, this model |

All applicable rows are evaluated and the tightest trips first, which is what
`Limiter.Check` already does. Owner-created rows are validated at write time to be at or
below the matching ceiling.

`expires_at` is a column because a key has exactly one; limits are rows because a key has
zero to many, over several metrics, periods and models. A platform key therefore never has
a row in `llm_gateway_rate_limits`: the constraint split is enforced on the write path
(below), not by the schema, since the gateway never resolves a platform key in the first
place.

`llm_gateway_usage` gains `api_key_id text NOT NULL DEFAULT ''`. The existing `token_id`
column holds the token *name*, which is not stable across renames and not unique across
users, so per-key usage needs the id. History backfills as `''`: per-key spend starts at
deploy day, not at the key's creation.

The counter key widens to
`rl:{tenant}:{user|-}:{key|-}:{model|-}:{metric}:{period}:{bucket}`. Every live Redis
counter resets once on deploy: a one-window blip, no data loss.

## Enforcement

```
GATEWAY PATH                                 EXCHANGE PATH
Authorization: Bearer sk-nb-gw-...           POST /api/auth/token {email, secret}
        |                                            |
  auth.Validate (auth/user_auths.go:54)        token.ts
    sha256 -> row                                bcrypt match row
    + AND kind IN ('gateway','all')      NEW     + reject kind='gateway'    NEW -> 401
    + AND expires_at > now()  (tz fixed)         + reject expires_at < now() NEW -> 401
    + SELECT id -> Identity.APIKeyID     NEW            |
        |                                        mint 1h session JWT -> /rpc/*
   routeStage        (model now known)
        |
   ratelimitStage    Scope{tenant, user, APIKeyID, Model}   NEW dimensions
        |            tightest matching row wins -> 429
   resolver -> filter -> provider
        |
   post-response: Reconcile(scope + model, tokens, cost)    NEW model argument
```

Files:

| File | Change |
|---|---|
| `llm/gateway/auth/auth.go:33` | `Identity` gains `APIKeyID` (stable uuid). `TokenID` stays the name for metering back-compat |
| `llm/gateway/auth/user_auths.go:74` | select `id`; add `kind IN ('gateway','all')` to the WHERE |
| `llm/gateway/ratelimit/types.go` | `Limit` and `Scope` gain `APIKeyID` and `Model`; `key()` widens |
| `llm/gateway/ratelimit/limiter.go:70,107` | `LimitsFor(tenant,user,key,model)`; `Reconcile` takes the model |
| `llm/gateway/ee/ratelimit/config.go:30` | index gains key and key+model buckets; loader selects the new columns |
| `llm/gateway/proxy/stages.go:70` | scope built from `rc.Identity.APIKeyID` and `rc.Model` |
| `llm/gateway/proxy/handler.go:531` | `Reconcile` receives `ev.Model`; metering event carries `api_key_id` |
| `app/src/pages/api/auth/token.ts:56` | reject gateway-kind rows and expired rows. Today it checks `status` only and ignores `expires_at`, which the query already returns |
| `api-server/services/user/service.go:465` | `CreateUserAuthToken` takes kind/expiry/limits, validates ceilings, writes limit rows in the same transaction |

Unchanged on purpose:

- Auth failures stay fail-closed (401).
- Limiter outages stay fail-open (`limiter.go`, deliberate). A per-key budget is therefore
  not a hard guarantee: Redis down means limits are not enforced. Stated here because
  "this key has a budget" reads stronger than what the system promises.

## API and UI

RPC actions (`app/src/lib/actions.yaml`, verbs per `docs/rpc-action-naming.md`):

| Action | Change |
|---|---|
| `users_create_token` | payload gains `kind`, `expires_at` (nullable = never), `limits[]`. An omitted `kind` mints `all`, so a client predating the split keeps exactly the access it has. `limits[]` on `kind='platform'` returns 400 (D8) |
| `users_list_token` | response gains `kind`, `expires_at`, and for gateway-reaching keys (`gateway`, `all`) `limits[]` plus current-period `usage{requests, tokens, cost_usd}` |
| `users_delete_token` | accepts `id`; name kept for back-compat |
| `users_update_token` | new. Edits expiry and limits on an existing key. The secret is never re-issued |
| gateway usage aggregate (`actions.yaml:879`) | gains `api_key_id` filter and group-by |

Limit rows are written by api-server directly into `llm_gateway_rate_limits`, which shares
the metastore and whose migrations api-server already owns. No cross-service call. The
gateway's `LimitStore` refreshes on a 30s ticker, so a new key's limits become live within
30s; the create dialog says so rather than the system busting a cache.

Ceiling validation is server-side in `CreateUserAuthToken`, reading the `api_key_id=''`
rows. The UI only pre-fills the maximum.

UI, `app/src/components/common/settings/ApiTokens.jsx` (409 lines today):

```
Settings -> API Tokens
[ Platform keys ]  [ AI Gateway keys ]

Name        Created    Last used   Expires    [revoke]
AI Gateway tab adds per row:
  claude-code   $12.40 / $20 this month  [########--] 80%
  expand: per-model caps, remaining quota
```

| Create-dialog field | Platform key | Gateway key |
|---|---|---|
| Name | yes | yes |
| Expires (30/90/365/never) | yes | yes |
| Limits (requests/tokens/cost x period) | no | yes, capped at ceiling |
| Per-model caps | no | yes, models from the tenant catalog (#36802) |

The file splits into `ApiTokens.jsx` (shell and tabs), `TokenList.jsx` and
`CreateTokenDialog.jsx`. It is gaining three tabs and a limits editor, and it is the file
being changed.

`ConnectView.tsx` mints `kind='gateway'` only and exposes the same expiry and limit fields.

## Rollout

Deploy order is load-bearing. The gateway must understand `kind` before api-server can mint one.

1. Migration job: `user_auths.kind`, `expires_at` type change, the two new
   `llm_gateway_rate_limits` columns, `llm_gateway_usage.api_key_id`. Everything keeps
   working on the app API: every row is `platform`, every new column is `''`.
2. Gateway: reads `kind`, enforces expiry, applies key and model limits.
3. api-server: mints kinds, validates ceilings, writes limit rows.
4. app: two-tab UI and create dialog.

Steps 2-4 may be separate PRs. Step 1 lands first and alone.

Migration rules that apply (see root `CLAUDE.md`):

- Scaffold with `./api-server/migrations/new-migration.sh`, never by hand, or `atlas.sum`
  goes stale and the deploy Job fails.
- `ALTER COLUMN expires_at TYPE timestamptz` avoids a table rewrite only when the server's
  `TimeZone` is UTC; otherwise it rewrites `user_auths` under ACCESS EXCLUSIVE. The table
  holds one row per auth, so the worst case is sub-second. The expected lock window goes in
  a comment at the top of the file.
- The `llm_gateway_usage` index is pre-built out of band with
  `CREATE INDEX CONCURRENTLY IF NOT EXISTS` on each live database before merge; the
  migration carries plain `CREATE INDEX IF NOT EXISTS`, a no-op where pre-applied.
  `CONCURRENTLY` inside migration SQL is rejected by `migrations-lint.yaml` unconditionally.
- On-prem installs run the same Job with no operator present and pay the plain index build.

No feature flag. The migration defaults every existing key to `all`, so nothing a tenant
holds today changes behaviour and there is no breakage to stage behind a toggle. The screen
ships enabled for everyone.

## Testing

| Level | Cases |
|---|---|
| Go unit (`auth/user_auths_test.go`) | gateway key accepted, platform key rejected, expired rejected, unknown rejected |
| Go unit (`ratelimit/limiter_test.go`) | key-scoped and model-scoped limits trip; tightest wins; `''` still means any |
| Go unit (api-server) | ceiling validation rejects an owner value above the tenant/user ceiling; a platform create carrying `limits[]` returns 400 |
| TS unit (`token.ts`) | gateway-kind -> 401, expired -> 401, platform -> JWT, all -> JWT |
| Observed behavior | mint a gateway key with a $0.01/month cost cap, issue two real `/v1/chat/completions` calls, capture the 429 body and the `llm_gateway_usage` row carrying `api_key_id`. Evidence goes in the PR body |

Validation before every commit: `make validate` in `llm/gateway` and `api-server/services`;
`npm run lint2 && npm run test` in `app`.

## Open items

- #34151 covers expiry only. The type split, the key and model limit dimensions, and
  per-key usage have no open issue. Tickets are needed under epic #33932 before those PRs
  can target `main`. To be created explicitly, not auto-generated.
- Where the per-key model picker gets its names is undecided. Two catalogs exist and do not
  hold the same names: `ai_list_models` (llm-server, used by the workflow pickers) and the
  gateway's own tenant catalog (`GET /v1/models`, built from `integrations` rows of type
  `llm_gateway`). Only the second is what the gateway matches on the wire, so a cap naming a
  model from the first would silently never apply. No RPC action exposes the gateway catalog
  to the app today. To be settled before the per-model caps are built.
- `users_create_token` grants create to `tenant_admin_readonly` and `account_admin_readonly`
  (`actions.yaml:5899`). A read-only role minting credentials is odd even though the key
  inherits the same read-only roles. Tracked under #35441, not here.
