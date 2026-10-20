import { queryGraphQL } from '@lib/HttpService';
import apiCloudAccount from '../index';

jest.mock('@lib/HttpService', () => ({
  queryGraphQL: jest.fn(),
  gqlStringify: jest.fn((v) => JSON.stringify(v)),
}));

const query = queryGraphQL as jest.Mock;

beforeEach(() => {
  query.mockReset().mockResolvedValue({ data: { data: { cloud_resource_v2: { rows: [] } } } });
});

// The resource tables (EC2, RDS, ECS, S3, CloudFoundry) show both Active and Inactive
// resources — buildStateApiParams defaults the "All" state to ['Active','Inactive']. The
// tag filter options must query the same set, otherwise a tag carried only by a stopped/
// terminated resource never appears as a filter option. Regression guard for #38658.
describe('tag filter options include stopped/inactive resources', () => {
  it('getDistinctTagKeys queries both Active and Inactive', async () => {
    await apiCloudAccount.getDistinctTagKeys('acct', 'AmazonEC2');
    expect(query.mock.calls[0][2].where.status).toEqual({ _in: ['Active', 'Inactive'] });
  });

  it('getDistinctTagValues queries both Active and Inactive', async () => {
    await apiCloudAccount.getDistinctTagValues('acct', 'tier', 'AmazonEC2');
    expect(query.mock.calls[0][2].where.status).toEqual({ _in: ['Active', 'Inactive'] });
  });
});
