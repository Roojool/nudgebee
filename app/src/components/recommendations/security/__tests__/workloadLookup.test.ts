import { queryGraphQL } from '@lib/HttpService';
import { lookupWorkloadId } from '@components/recommendations/security/findingOwnership';

// The real list builder runs; only the wire call is stubbed, so this proves the
// filters reach the query rather than that the lookup was called with them.
jest.mock('@lib/HttpService', () => ({
  ...jest.requireActual('@lib/HttpService'),
  queryGraphQL: jest.fn(),
}));

const query = queryGraphQL as jest.Mock;

beforeEach(() => {
  query.mockReset();
});

describe('lookupWorkloadId', () => {
  it('filters the workload list by account, namespace, exact name and kind', async () => {
    query.mockResolvedValue({ data: { data: { k8s_workloads: { rows: [{ cloud_resource_id: 'wl-res-1' }] } } } });

    const id = await lookupWorkloadId({ accountId: 'acct-1', namespace: 'payments', workloadName: 'checkout', workloadType: 'Deployment' });

    expect(id).toBe('wl-res-1');
    expect(query).toHaveBeenCalledTimes(1);
    const [gql, , vars] = query.mock.calls[0];
    expect(gql).toContain('account_id:{_eq:"acct-1"}');
    expect(gql).toContain('namespace:{_eq:"payments"}');
    expect(gql).toContain('name:{_eq:"checkout"}');
    expect(gql).toContain('kind:{_eq:"Deployment"}');
    expect(gql).not.toContain('_ilike');
    expect(vars).toEqual({ limit: 1, offset: 0 });
  });

  it('skips ReplicaSets rather than guessing a kind when none is known', async () => {
    query.mockResolvedValue({ data: { data: { k8s_workloads: { rows: [] } } } });

    const id = await lookupWorkloadId({ accountId: 'acct-1', namespace: 'payments', workloadName: 'checkout' });

    expect(id).toBeNull();
    expect(query.mock.calls[0][0]).toContain('kind:{_neq:"ReplicaSet"}');
  });

  it('does not query without a namespace or workload name', async () => {
    expect(await lookupWorkloadId({ accountId: 'acct-1', workloadName: 'checkout' })).toBeNull();
    expect(await lookupWorkloadId({ accountId: 'acct-1', namespace: 'payments' })).toBeNull();
    expect(query).not.toHaveBeenCalled();
  });
});
