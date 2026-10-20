import type { NextApiResponse } from 'next';
import handler from '@pages/api/auth/token';
import { getUserByUsernameAndAccountProviderAndCredential } from '@lib/UserService';

jest.mock('@lib/UserService', () => ({
  getUserByUsernameAndAccountProviderAndCredential: jest.fn(),
  updateUserAccountAccessed: jest.fn().mockResolvedValue({}),
}));
jest.mock('@lib/internal', () => ({
  validateHashedPassword: jest.fn().mockResolvedValue(true),
  encodeSessionJWT: jest.fn().mockResolvedValue('jwt'),
  encrypt: jest.fn().mockResolvedValue('encrypted-jwt'),
}));

const mockGetUser = getUserByUsernameAndAccountProviderAndCredential as jest.Mock;

function makeRes() {
  const res = {
    statusCode: 0,
    body: undefined as any,
    status(code: number) {
      res.statusCode = code;
      return res;
    },
    json(payload: any) {
      res.body = payload;
      return res;
    },
  };
  return res as unknown as NextApiResponse & typeof res;
}

function authRow(overrides: Record<string, unknown>) {
  return {
    data: {
      user_auths: [
        {
          id: 'key-1',
          status: 'active',
          tenant_id: 'tenant-1',
          credential: 'bcrypt-hash',
          kind: 'platform',
          expires_at: null,
          user: { id: 'user-1', username: 'a@b.com', display_name: 'A', status: 'active', user_roles: [] },
          ...overrides,
        },
      ],
    },
  };
}

const req = { method: 'POST', body: { email: 'a@b.com', secret: 'sk-nb-abc' } } as any;

test('a platform key still mints a session JWT', async () => {
  mockGetUser.mockResolvedValue(authRow({}));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(200);
  expect(res.body.token).toBe('encrypted-jwt');
});

test('an AI Gateway key cannot be exchanged', async () => {
  mockGetUser.mockResolvedValue(authRow({ kind: 'gateway' }));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(401);
  expect(res.body.message).toMatch(/gateway/i);
});

test('an expired key cannot be exchanged', async () => {
  mockGetUser.mockResolvedValue(authRow({ expires_at: '2020-01-01T00:00:00Z' }));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(401);
  expect(res.body.message).toMatch(/expired/i);
});

test('a platform key is exchanged for a session', async () => {
  mockGetUser.mockResolvedValue(authRow({ kind: 'platform' }));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(200);
});

// The guard rejects kind === 'gateway' rather than allow-listing 'platform', so a
// an all-access key passing is load-bearing behaviour, not an accident of the condition.
test('an all-access key is exchanged for a session', async () => {
  mockGetUser.mockResolvedValue(authRow({ kind: 'all' }));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(200);
});

// Scopes reach the gateway through the session: a scoped key's JWT must carry them, and a
// full-access key's must not (absence is what means full access).
const encodeSessionJWT = jest.requireMock('@lib/internal').encodeSessionJWT as jest.Mock;

test('a scoped key carries its scopes into the session claims', async () => {
  encodeSessionJWT.mockClear();
  mockGetUser.mockResolvedValue(authRow({ scopes: ['tickets:Read', 'tickets:Write'] }));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(200);
  expect(encodeSessionJWT.mock.calls[0][1].token_scopes).toEqual(['tickets:Read', 'tickets:Write']);
});

test('a full-access key carries no token_scopes claim', async () => {
  encodeSessionJWT.mockClear();
  mockGetUser.mockResolvedValue(authRow({ scopes: null }));
  const res = makeRes();
  await handler(req, res);
  expect(res.statusCode).toBe(200);
  expect(encodeSessionJWT.mock.calls[0][1]).not.toHaveProperty('token_scopes');
});
