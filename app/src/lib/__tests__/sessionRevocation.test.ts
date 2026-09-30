// The revocation check runs before every cookie request. When services-server
// cannot be reached (a pod that exited while still in the Service's endpoints)
// the check must give up quickly and fail open, not hold the request for
// undici's 10 s connect timeout (#39746).
jest.mock('next-auth/jwt', () => ({ getToken: jest.fn() }));
jest.mock('@lib/internal', () => ({ decodeSessionJWT: jest.fn(), decrypt: jest.fn() }));
jest.mock('@lib/HttpService', () => ({ queryGraphQL: jest.fn() }));

import { queryGraphQL } from '@lib/HttpService';
import { bypassGraphQLAsServer } from '@lib/rpcGateway';
import { isSessionRevoked } from '@lib/sessionRevocation';

const queryGraphQLMock = queryGraphQL as jest.Mock;

describe('isSessionRevoked', () => {
  afterEach(() => {
    queryGraphQLMock.mockReset();
    jest.restoreAllMocks();
  });

  it('bounds the check with a one-second timeout signal', async () => {
    queryGraphQLMock.mockResolvedValue({ data: { data: { auth_check_session: { revoked: false } } } });
    const timeoutSpy = jest.spyOn(AbortSignal, 'timeout');

    await isSessionRevoked('u1', null, 1700000000);

    expect(timeoutSpy).toHaveBeenCalledWith(1000);
    const signal = queryGraphQLMock.mock.calls[0][4];
    expect(signal).toBe(timeoutSpy.mock.results[0].value);
  });

  it('fails open when the check comes back with an error', async () => {
    jest.spyOn(console, 'error').mockImplementation(() => {});
    queryGraphQLMock.mockResolvedValue({
      data: { data: { auth_check_session: null }, errors: [{ message: 'Upstream unreachable for auth_check_session' }] },
    });

    await expect(isSessionRevoked('u1', null, 1700000000)).resolves.toBe(false);
  });

  it('reports a revoked session', async () => {
    queryGraphQLMock.mockResolvedValue({ data: { data: { auth_check_session: { revoked: true } } } });

    await expect(isSessionRevoked('u1', null, 1700000000)).resolves.toBe(true);
  });
});

describe('bypassGraphQLAsServer with a timeout signal', () => {
  const originalFetch = global.fetch;
  beforeEach(() => {
    process.env.SERVICE_API_SERVER_URL = 'http://api-server';
  });
  afterEach(() => {
    global.fetch = originalFetch;
    jest.restoreAllMocks();
  });

  it('stops waiting on a blackholed upstream and logs it as a timeout', async () => {
    const errorLog = jest.spyOn(console, 'error').mockImplementation(() => {});
    // Never answers until the fetch signal aborts it, the way undici rejects a
    // blackholed connect on abort.
    const fetchMock = jest.fn(
      (_url: string, init: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init.signal?.addEventListener('abort', () => reject(init.signal?.reason));
        })
    );
    global.fetch = fetchMock as unknown as typeof fetch;

    const result = await bypassGraphQLAsServer({
      query: 'mutation AuthCheckSession($object: auth_check_session_input!) { auth_check_session(object: $object) { revoked } }',
      variables: { object: { user_id: 'u1', jti: '', iat: 1700000000 } },
      traceparent: '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01',
      requestId: 'r1',
      signal: AbortSignal.timeout(20),
    });

    expect(fetchMock.mock.calls[0][0]).toBe('http://api-server/rpc/user');
    expect(result).toMatchObject({
      handled: true,
      body: { data: { auth_check_session: null }, errors: [{ message: 'Upstream unreachable for auth_check_session' }] },
    });
    expect(errorLog).toHaveBeenCalledWith(
      expect.stringContaining('upstream_unreachable method=auth_check_session url=http://api-server/rpc/user detail=timed out')
    );
  });
});
