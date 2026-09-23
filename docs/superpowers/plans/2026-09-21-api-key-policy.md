# API Key Policy Implementation Plan

**Goal:** Split NB API tokens into platform keys and AI Gateway keys, give both an optional expiry enforced on every auth path, and let a gateway-reaching key carry its own request/token/cost quotas, including per-model ones.

**Architecture:** A new `kind` column on `user_auths` (`platform` | `gateway` | `all`) decides which door a key opens; each auth path rejects the kinds it does not serve. The gateway's existing limiter gains two dimensions (`api_key_id`, `model`) on its config table and its counter keys, so a per-key or per-model cap is an ordinary limit row, not a new mechanism. Existing tokens default to `kind='all'` and keep the dual-use reach they were minted with.

**Tech Stack:** Go (api-server/services, llm/gateway — Gin, sqlx, sqlmock, testify), Postgres via Atlas migrations, TypeScript/Next.js + Jest (app), React (ApiTokens UI).

**Spec:** `docs/superpowers/specs/2026-09-21-api-key-policy-design.md`

> **As shipped.** Two earlier cuts were abandoned: a non-choosable `legacy` kind for
> pre-existing tokens (a bucket that only shrinks), then defaulting everything to
> `platform` (would have 401'd 16 live gateway callers on dev). `all` is a real, choosable
> kind instead. The feature flag was also dropped: nothing existing changes behaviour, so
> the screen ships enabled. Code listings were removed from this plan — the shipped code
> is the source of truth.

## Global Constraints

- Scaffold migrations with `./api-server/migrations/new-migration.sh <snake_case_name>`; after editing SQL run `--regen-sum`, or Migrations-Validate fails on a stale `atlas.sum`.
- No `CREATE INDEX CONCURRENTLY` in migration SQL (`migrations-lint.yaml` rejects it). Plain `CREATE INDEX IF NOT EXISTS`; pre-build out of band where the table is large.
- Validate before every commit: `make validate` in `llm/gateway` and `api-server/services`; `npm run lint2 && npm run test` in `app`.
- Commit format `type(scope): description`, lowercase scope (`nb-34151`).
- Deploy order is load-bearing: migration (Task 1) → gateway (Tasks 2-5) → api-server (Tasks 6-7) → app (Tasks 8-9).
- A create request omitting `kind` mints `all`, so older clients keep the access they had.
- Limiter outages stay fail-open; auth failures stay fail-closed. Do not "fix" either.
- Token length: bcrypt reads only 72 bytes and hashes the whole prefixed token, so longest prefix + hex random must stay ≤ 72.

---

### Task 1: Database migration

**Files:** `api-server/migrations/migrations/app/1790191876028_V964_api_key_policy.{up,down}.sql`, `atlas.sum` (regenerated).

**Produces:**
- `user_auths.kind` text not null default `'all'`.
- `user_auths.expires_at` as `timestamptz`. Guarded by an `information_schema` check — the `USING ... AT TIME ZONE 'UTC'` conversion is not idempotent and would shift every expiry if re-run.
- `llm_gateway_rate_limits.api_key_id`, `.model` and `llm_gateway_usage.api_key_id`, text not null default `''` (`''` = any).
- Unique index `(user, name, provider, kind)` replacing `(user, name, provider)`.
- Indexes `idx_llm_gw_ratelimits_key` and `idx_llm_gw_usage_api_key` (usage index pre-built out of band on live DBs).

**Verify:** apply locally; `\d user_auths` shows `kind | text | 'all'::text` and `expires_at | timestamp with time zone`. Re-apply once to exercise the guard — no expiry moves.

---

### Task 2: Gateway rejects the wrong key kind and returns the key id

**Files:** `llm/gateway/auth/auth.go` (`Identity`), `llm/gateway/auth/user_auths.go` (row struct + query), test `user_auths_test.go`.

**Produces:** `auth.Identity.APIKeyID` — the `user_auths.id` of the authenticating key. `TokenID` keeps holding the key *name*.

**Steps:**
1. Tests `TestUserAuthsValidator_ReturnsAPIKeyID`, `TestUserAuthsValidator_QueryFiltersGatewayKinds`; see them fail.
2. Add `APIKeyID` to `Identity`; select `id::text AS api_key_id`; add `AND kind IN ('gateway', 'all')`.
3. `cd llm/gateway && go test ./auth/ -v` passes (update the existing `ValidToken` mock rows for the new column).

---

### Task 3: Key and model dimensions in the limiter core

**Files:** `llm/gateway/ratelimit/types.go` (`Limit`, `Scope`, `key`), `ratelimit/limiter.go` (`LimitSource`, `Check`, `Reconcile`, scope label), test `limiter_test.go`.

**Produces:** `LimitsFor(tenant, user, apiKey, model string) []Limit`; `Scope{TenantID, UserID, APIKeyID, Model}`; `Limit.APIKeyID`, `Limit.Model`; counter key `rl:{tenant}:{user|-}:{key|-}:{model|-}:{metric}:{period}:{bucket}`.

**Steps:**
1. Tests: key-scoped cap, model-scoped cap only applies to that model, key counters are separate, reconcile respects the model guard.
2. Widen the types; in `Check` and `Reconcile` skip a row whose `Model` is set and differs from `Scope.Model`.
3. Scope label in the 429 message: `key+model`, `key`, `user`, `tenant`.
4. `go test ./ratelimit/ -v` passes, including every pre-existing test (new fields default to `""`).

---

### Task 4: EE limit store loads and indexes the new dimensions

**Files:** `llm/gateway/ee/ratelimit/config.go` (`limitIndex`, `newIndex`, `LimitsFor`, `DBLoader`), test `config_test.go` (new); update `guardrail_test.go` call sites.

**Produces:** a `LimitStore` satisfying the widened `LimitSource`.

**Steps:**
1. Tests `TestLimitStore_ReturnsKeyAndModelRows`, `TestLimitStore_UnknownKeyStillGetsTenantAndUserRows`.
2. Add `byKey` index keyed `tenant|user|api_key_id`; route rows with `APIKeyID != ""` there (model-scoped rows too — the limiter filters by model). `LimitsFor` merges tenant + user + key rows.
3. `DBLoader` selects `api_key_id, model`.
4. Leave `hasUserCostLimit` alone: a key-scoped cost row has `UserID` set, so a per-key cost cap already suppresses the default guardrail.

---

### Task 5: Wire the scope through the proxy and record the key id

**Files:** `llm/gateway/proxy/stages.go` (`ratelimitStage.Handle`), `proxy/handler.go` (metering + `Reconcile`), `metering/usage.go` (`UsageEvent`, `EventInput`, `NewEvent`), `metering/sink.go` (`usageColumns` + args); tests `sink_test.go`, `stages_test.go` (new).

**Produces:** `llm_gateway_usage.api_key_id` on every recorded request; quota checks scoped to the key and the *requested* model.

**Steps:**
1. Tests `TestNewEvent_CarriesAPIKeyID`, `TestUsageColumnsIncludeAPIKeyID`, `TestRatelimitStage_ScopesToAPIKeyAndRequestedModel`.
2. Carry `APIKeyID` on the usage event and insert the column.
3. Stage passes `APIKeyID: rc.Identity.APIKeyID, Model: rc.Model` — the model the client asked for, so a cap keeps meaning that name after an admin remaps it.
4. `Reconcile` uses the same scope with `ev.RequestedModel`; copy `APIKeyID` into both `recordReject*` paths.
5. `make validate` in `llm/gateway`.

---

### Task 6: Mint typed keys with expiry and limits

**Files:** `api-server/services/user/model.go`, `service.go` (`CreateUserAuthToken`), `token_policy.go` (new), test `token_policy_test.go` (new).

**Produces:** `UserTokenCreateRequest{Name, Kind, ExpiresAt *string, Limits []TokenLimit}`; `TokenLimit{Metric, Period, Value, Model}`; helpers `normalizeKind`, `carriesGatewayTraffic`, `validateLimitsForKind`, `tightestCeiling`, `validateLimitsAgainstCeilings`.

**Steps:**
1. Pure tests for each helper, plus `TestLongestTokenFitsBcryptLimit`.
2. Helpers: `""` → `all`; unknown kind → 400; limits on a `platform` key → 400 (not silently dropped); metric ∈ requests|tokens|cost, period ∈ minute|hour|day|month, value > 0; an owner limit above the tightest tenant/user ceiling → 400.
3. Resolve `kind` before generating the secret. Prefix `sk-nb-gw-` for `gateway`, `sk-nb-` otherwise. Random part `TokenRandomBytes = 28` (9 + 56 = 65 ≤ 72).
4. `expires_at` must be RFC3339 and in the future.
5. One transaction: insert the `user_auths` row `RETURNING id`, then one `llm_gateway_rate_limits` row per limit with that `api_key_id`.
6. `go test ./user/ -v` (DB-backed tests skip without `testenv`); `make validate`.

---

### Task 7: List, delete and update typed keys

**Files:** `api-server/services/user/model.go`, `service.go` (`DeleteUserAuthToken`, `ListUserAuthTokens`, new `UpdateUserAuthToken`), `api/actions_user.go` (`users_update_token` case), tests in `token_policy_test.go`.

**Produces:** `UserTokenDeleteRequest{Id, Name}`; `UserTokenUpdateRequest{Id, ExpiresAt, Limits}`; list rows carrying `kind`, `expires_at`, `limits`, `usage`.

**Steps:**
1. Tests `TestDeleteRequestNeedsAnIdentifier`, `TestResolveDeleteID`.
2. Delete by id. A name that matches more than one kind → 400 "ambiguous; delete by id". Delete the key's limit rows in the same transaction.
3. List: one query for all keys' limits and one for this calendar month's spend (UTC window, matching the limiter's month bucket), fanned out in memory.
4. Update: load the key's kind, run the same kind and ceiling checks as create, update `expires_at`, replace limits wholesale. The secret is never re-issued.
5. Route `users_update_token`; comma-ok on `tenantRequest["user"]` (400 on a non-object, no panic). Audit the change.

---

### Task 8: The exchange path rejects gateway keys and expired keys

**Files:** `api-server/services/query/metadata.go` (`kind` on `user_auth_by_username_v2`), `app/src/lib/UserService.ts`, `app/src/lib/actions.yaml` (new `users_update_token`), `app/src/api1/user/index.js`, `app/src/pages/api/auth/token.ts`; test `app/__tests__/pages/api/auth/token.test.ts` (new).

**Produces:** `apiUser.createUserToken({name, kind, expiresAt, limits})`, `deleteUserToken({id, name})`, `updateUserToken({id, expiresAt, limits})`.

**Steps:**
1. Tests: platform and all keys mint a session JWT; gateway key → 401; expired key → 401.
2. Return `kind` from the auth query; in `token.ts` reject `kind === 'gateway'` and a past `expires_at` before minting the JWT.
3. Widen the GraphQL documents and client functions; update every `createUserToken` / `deleteUserToken` call site (`grep -rn` in `app/src`).
4. `npm run dev` boots (parses `actions.yaml`); `npm run lint2`.

---

### Task 9: Token screen with key types, expiry and limits

**Files:** `app/src/components/common/settings/ApiTokens.jsx` (shell + tabs), `TokenList.jsx` (new), `CreateTokenDialog.jsx` (new), `keyKinds.js` (new), `app/src/components/llm/gateway-usage/views/ConnectView.tsx`; test `ApiTokens.test.jsx`.

**Steps:**
1. Tests: three tabs; each tab lists only its kind; platform create sends no limits; all-access create sends `kind=all` with limits; zero-value limit rows filtered; custom expiry in hours; Create disabled on an empty/zero custom duration; revoke by id.
2. `keyKinds.js`: JS mirror of `carriesGatewayTraffic` + labels — one rule for UI, Go and SQL.
3. `TokenList`: Name, Created, Last used, Expires; gateway-reaching tabs add Limits and This month (`cost_usd ?? 0`).
4. `CreateTokenDialog`: name; expiry 30/90/365 days, Never, Custom (minutes/hours/days); limit editor only when the kind reaches the gateway. Hold limit values as strings while editing, coerce on submit. Model is free text for now.
5. `ApiTokens`: tabs Platform / AI Gateway / All-access; the active tab is the kind Create mints, so `all` is never picked by accident. `defaultKind` prop; `ConnectView` opens on `gateway`.
6. Exercise the real flow: mint one key per kind from the UI; confirm prefixes, the 401/200 matrix, and `api_key_id` on usage rows. Keep outputs for the PR body.

---

## Verification checklist

- [ ] Platform key: 401 at `/v1/*`, JWT at `/api/auth/token`.
- [ ] Gateway key: 401 at `/api/auth/token`, passes at `/v1/*`.
- [ ] All key: passes on both.
- [ ] Expired key: 401 on both paths.
- [ ] Per-key cap trips with a 429 naming `key`; per-model cap names `key+model` and leaves other models alone.
- [ ] Platform create carrying `limits[]` → 400.
- [ ] Owner limit above the tenant ceiling → 400, on create and on update.
- [ ] Revoking a key removes its `llm_gateway_rate_limits` rows.
- [ ] Pre-existing tokens behave exactly as before (they are `all`).
