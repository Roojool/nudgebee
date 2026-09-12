import type { NextApiRequest, NextApiResponse } from 'next';
import handler, { httpStatusFor, parseComponentQuery, selectComponents, type StatusPayload } from '../status';

const payload: StatusPayload = {
  status: 'degraded',
  checked_at: '2026-09-12T00:00:00.000Z',
  components: [
    { id: 'dashboard', name: 'Dashboard', status: 'operational' },
    { id: 'api', name: 'API', status: 'operational' },
    { id: 'ai', name: 'Nubi AI', status: 'outage', reason: 'health check timed out' },
    { id: 'automation', name: 'Automation', status: 'degraded', reason: 'Temporal: connection refused' },
    { id: 'notifications', name: 'Notifications', status: 'maintenance' },
    { id: 'ingest', name: 'Data Ingest', status: 'unknown', reason: 'not configured in this environment' },
  ],
};

describe('parseComponentQuery', () => {
  it('returns nothing when the param is absent, so the full payload is served', () => {
    expect(parseComponentQuery(undefined)).toEqual([]);
  });

  it('splits a comma list and trims each id', () => {
    expect(parseComponentQuery(' api, ai ')).toEqual(['api', 'ai']);
  });

  it('accepts the param repeated and de-duplicates', () => {
    expect(parseComponentQuery(['api', 'ai,api'])).toEqual(['api', 'ai']);
  });
});

describe('selectComponents', () => {
  it('drops everything but the requested components and recomputes the rollup over them only', () => {
    const { payload: narrowed, missing } = selectComponents(payload, ['api', 'dashboard']);
    expect(missing).toEqual([]);
    expect(narrowed.components.map((c) => c.id)).toEqual(['api', 'dashboard']);
    // The AI outage elsewhere must not leak into a healthy subset.
    expect(narrowed.status).toBe('operational');
  });

  it('reports an outage when the single requested component is down', () => {
    expect(selectComponents(payload, ['ai']).payload.status).toBe('outage');
  });

  it('reports degraded when a subset mixes healthy and failing components', () => {
    expect(selectComponents(payload, ['api', 'ai']).payload.status).toBe('degraded');
  });

  it('reports maintenance for a component under a maintenance notice', () => {
    expect(selectComponents(payload, ['notifications']).payload.status).toBe('maintenance');
  });

  it('reports unknown when the only requested component is not being checked', () => {
    expect(selectComponents(payload, ['ingest']).payload.status).toBe('unknown');
  });

  it('names ids that match no component so the caller can refuse the request', () => {
    const { payload: narrowed, missing } = selectComponents(payload, ['api', 'billing', 'search']);
    expect(missing).toEqual(['billing', 'search']);
    expect(narrowed.components.map((c) => c.id)).toEqual(['api']);
  });
});

describe('httpStatusFor', () => {
  it('answers 200 only for operational and planned maintenance', () => {
    expect(httpStatusFor('operational')).toBe(200);
    expect(httpStatusFor('maintenance')).toBe(200);
    expect(httpStatusFor('degraded')).toBe(503);
    expect(httpStatusFor('outage')).toBe(503);
    expect(httpStatusFor('unknown')).toBe(503);
  });
});

// The handler runs the real collector. With no service URLs in the environment
// every probe reports `unknown` and only the dashboard is operational, which is
// enough to cover the three HTTP outcomes without any network. next/jest loads
// .env files, so the URLs are blanked explicitly rather than assumed absent.
describe('handler with ?component', () => {
  const PROBE_ENV = [
    'SERVICE_API_SERVER_URL',
    'RELAY_SERVER_ENDPOINT',
    'LLM_SERVER_URL',
    'WORKFLOW_SERVER_URL',
    'TEMPORAL_GRPC_ENDPOINT',
    'NOTIFICATION_SERVICE_URL',
    'STATUS_NOTICE_FILE',
  ];
  const saved: Record<string, string | undefined> = {};

  beforeAll(() => {
    for (const key of PROBE_ENV) {
      saved[key] = process.env[key];
      delete process.env[key];
    }
  });

  afterAll(() => {
    for (const key of PROBE_ENV) {
      if (saved[key] === undefined) delete process.env[key];
      else process.env[key] = saved[key];
    }
  });

  function call(query: Record<string, string | string[]>) {
    const headers: Record<string, string> = {};
    const out: { code?: number; body?: unknown } = {};
    const res = {
      setHeader: (k: string, v: string) => {
        headers[k] = v;
      },
      status: (code: number) => ({
        json: (body: unknown) => {
          out.code = code;
          out.body = body;
        },
      }),
    } as unknown as NextApiResponse;
    return handler({ method: 'GET', query } as unknown as NextApiRequest, res).then(() => ({ ...out, headers }));
  }

  it('answers 200 with only the requested component when it is healthy', async () => {
    const { code, body } = await call({ component: 'dashboard' });
    expect(code).toBe(200);
    expect((body as StatusPayload).components.map((c) => c.id)).toEqual(['dashboard']);
    expect((body as StatusPayload).status).toBe('operational');
  });

  it('answers 503 when the requested component is not healthy', async () => {
    const { code, body } = await call({ component: 'api' });
    expect(code).toBe(503);
    expect((body as StatusPayload).components[0].status).toBe('unknown');
  });

  it('answers 400 when the parameter is present but names nothing', async () => {
    const { code, body } = await call({ component: '' });
    expect(code).toBe(400);
    expect(body).toEqual({ error: 'component is empty' });
  });

  it('answers 404 naming the ids that do not exist', async () => {
    const { code, body } = await call({ component: 'dashboard,billing' });
    expect(code).toBe(404);
    expect(body).toEqual({ error: 'unknown component', unknown: ['billing'] });
  });

  it('keeps the cache header on narrowed answers so probers do not amplify load', async () => {
    const { headers } = await call({ component: 'dashboard' });
    expect(headers['Cache-Control']).toMatch(/max-age=/);
  });
});
