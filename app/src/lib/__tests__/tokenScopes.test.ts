// Scoped API keys (user_auths.scopes) at the gateway. A scope NARROWS: the call must
// clear the owner's role/grant gate AND match one of the key's scopes. These tests pin
// the three properties that make that safe — a scope never widens, credential and role
// management is unreachable whatever the scopes, and no bypass (super-admin,
// tenant-agnostic) skips the check.
//
// Same module-edge stubs as rbacEnforcementMatrix.test.ts: jose ships browser ESM jest
// can't parse, and none of these participate in the gate.
jest.mock('next-auth/jwt', () => ({ getToken: jest.fn() }));
jest.mock('@lib/internal', () => ({ decodeSessionJWT: jest.fn(), decrypt: jest.fn() }));
jest.mock('@lib/sessionRevocation', () => ({ isSessionRevoked: jest.fn() }));

import type { JWT } from 'next-auth/jwt';
import { actionAllowedByTokenScopes, buildSessionVariables, forwardAction } from '@lib/rpcGateway';
import { loadRpcRoutes } from '@lib/rpcRoutes';
import { buildCatalog, isTokenScopeBlockedAction } from '@lib/permissionCatalog';

const routes = loadRpcRoutes();
const actionNames = Object.keys(routes);

// Every box the key-scope picker can render (same filter as /api/permissions/token-catalog).
const tokenCatalog = buildCatalog(actionNames.filter((n) => !isTokenScopeBlockedAction(n)));
const everyScope = tokenCatalog.flatMap((e) => e.classes.map((c) => `${e.module}:${c}`));

const CREDENTIAL_LIKE = /(^|_)(tokens?|sessions?|roles?|credentials?|passwords?)(_|$)/;

describe('token scopes — credential and role management is unreachable', () => {
  // The escalation this guards: users_create_token classifies as users:Write, so a key
  // scoped to users:Write could otherwise mint itself an unrestricted key.
  it.each(['users_create_token', 'users_delete_token', 'users_update_token', 'users_list_token'])(
    'refuses %s even with every scope ticked',
    (name) => {
      expect(routes[name]).toBeDefined();
      expect(actionAllowedByTokenScopes(name, routes[name], everyScope)).toBe(false);
    }
  );

  // Derived from the live inventory, so an action added later with one of these words in
  // its name is covered without anyone updating a list.
  it('admits no credential-, session- or role-named action with every scope ticked', () => {
    const reachable = actionNames.filter((n) => CREDENTIAL_LIKE.test(n) && actionAllowedByTokenScopes(n, routes[n], everyScope));
    expect(reachable).toEqual([]);
  });

  it('admits nothing from the privilege-administration modules', () => {
    const privileged = actionNames.filter((n) => /^(customroles|userroles|auth|signup|roles)_/.test(n));
    expect(privileged.length).toBeGreaterThan(0);
    expect(privileged.filter((n) => actionAllowedByTokenScopes(n, routes[n], everyScope))).toEqual([]);
  });

  it('keeps blocked actions out of the picker catalog', () => {
    const offered = tokenCatalog.flatMap((e) => Object.values(e.actions).flatMap((list) => (list ?? []).map((a) => a.name)));
    expect(offered.filter((n) => isTokenScopeBlockedAction(n))).toEqual([]);
    expect(offered).not.toContain('users_create_token');
  });
});

describe('token scopes — matching', () => {
  const readAction = actionNames.find((n) => routes[n].permission === 'tickets:Read' && !isTokenScopeBlockedAction(n))!;
  const k8sAction = actionNames.find((n) => routes[n].permission?.startsWith('k8s:') && !isTokenScopeBlockedAction(n))!;

  it('admits an action its scope covers, and Write covers Read like a custom grant', () => {
    expect(actionAllowedByTokenScopes(readAction, routes[readAction], ['tickets:Read'])).toBe(true);
    expect(actionAllowedByTokenScopes(readAction, routes[readAction], ['tickets:Write'])).toBe(true);
  });

  it('refuses an action in another module', () => {
    expect(actionAllowedByTokenScopes(k8sAction, routes[k8sAction], ['tickets:Read', 'tickets:Write'])).toBe(false);
  });

  it('refuses every action for an empty scope list', () => {
    expect(actionNames.filter((n) => actionAllowedByTokenScopes(n, routes[n], []))).toEqual([]);
  });

  it('never honours an account-scoped (scoped:) key as a token scope on a custom handler', () => {
    const custom = actionNames.find((n) => routes[n].permission && !routes[n].handler.endsWith('/rpc/query') && !isTokenScopeBlockedAction(n))!;
    expect(actionAllowedByTokenScopes(custom, routes[custom], [`scoped:${routes[custom].permission}`])).toBe(false);
  });
});

describe('token scopes — forwardAction', () => {
  const forward = (method: string, roles: string[], tokenScopes?: string[]) =>
    forwardAction({
      method,
      params: {},
      sessionVariables: {
        role: roles[0] || 'tenant_admin_readonly',
        allowed_roles: roles,
        permissions: [],
        user_id: 'u1',
        tenant_id: 't1',
        ...(tokenScopes ? { token_scopes: tokenScopes } : {}),
      },
      tenantId: 't1',
      userId: 'u1',
      traceparent: '',
      requestId: 'r1',
    });

  const k8sAction = actionNames.find((n) => routes[n].permission?.startsWith('k8s:') && !isTokenScopeBlockedAction(n))!;

  it('denies a tenant admin whose key is scoped elsewhere', async () => {
    const r = await forward(k8sAction, ['tenant_admin'], ['tickets:Read']);
    expect(!r.ok && r.error.kind).toBe('token_scope_denied');
  });

  it('denies users_create_token to a tenant admin holding a users:Write key', async () => {
    const r = await forward('users_create_token', ['tenant_admin'], ['users:Read', 'users:Write']);
    expect(!r.ok && r.error.kind).toBe('token_scope_denied');
  });

  it('is not skipped for super-admin or tenant-agnostic actions', async () => {
    expect(routes.users_list_tenants.tenantAgnostic).toBe(true);
    const agnostic = await forward('users_list_tenants', ['tenant_admin'], ['tickets:Read']);
    expect(!agnostic.ok && agnostic.error.kind).toBe('token_scope_denied');
    const superAdmin = await forward(k8sAction, ['super_admin'], ['tickets:Read']);
    expect(!superAdmin.ok && superAdmin.error.kind).toBe('token_scope_denied');
  });

  it('never widens: a covered action still needs the owner to hold a role for it', async () => {
    const r = await forward(k8sAction, [], [routes[k8sAction].permission!]);
    expect(!r.ok && r.error.kind).toBe('no_tenant_role');
  });

  it('leaves an unscoped key (no token_scopes) exactly as before', async () => {
    // Stub the upstream so the result does not depend on whether a handler URL is set
    // in the environment: without one the call stops at handler_unresolved, with one it
    // reaches fetch. Either way the gate must have let it through.
    const fetchSpy = jest.spyOn(global, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      headers: { get: () => 'application/json' },
      text: async () => '{}',
    } as unknown as Response);
    try {
      const r = await forward('users_create_token', ['tenant_admin']);
      expect(!r.ok && r.error.kind).not.toBe('token_scope_denied');
    } finally {
      fetchSpy.mockRestore();
    }
  });
});

describe('token scopes — session variables', () => {
  const jwt = (extra: Record<string, unknown>) => ({ id: 'u1', roles: ['tenant_admin'], tenant: { id: 't1' }, ...extra } as unknown as JWT);

  it('carries the key scopes when the session came from a scoped key', () => {
    expect(buildSessionVariables(jwt({ tokenScopes: ['tickets:Read'] })).token_scopes).toEqual(['tickets:Read']);
  });

  it('carries an empty list as-is (allows nothing), not as full access', () => {
    expect(buildSessionVariables(jwt({ tokenScopes: [] })).token_scopes).toEqual([]);
  });

  it('omits token_scopes for a cookie session or an unscoped key', () => {
    expect(buildSessionVariables(jwt({})).token_scopes).toBeUndefined();
  });
});
