import { accountFilterFromQuery, filtersToUrlQuery } from '../utils';

// Links into Optimise name the account two ways. The page's own filter writes
// `account`; the cluster, workload and home pages send `accountId`, which the
// page kept for its permission check but never applied to the list — so those
// links opened on every account.
describe('accountFilterFromQuery', () => {
  it('selects the account a cluster or home page link names', () => {
    expect(accountFilterFromQuery({ accountId: 'acct-1' })).toEqual(['acct-1']);
  });

  it("selects the account the page's own filter names", () => {
    expect(accountFilterFromQuery({ account: 'acct-1' })).toEqual(['acct-1']);
  });

  it('lets the filter spelling win when a link carries both', () => {
    expect(accountFilterFromQuery({ account: 'acct-2', accountId: 'acct-1' })).toEqual(['acct-2']);
  });

  it('keeps every account of a multi-account selection', () => {
    expect(accountFilterFromQuery({ account: ['acct-1', 'acct-2'] })).toEqual(['acct-1', 'acct-2']);
    expect(accountFilterFromQuery({ accountId: ['acct-1', 'acct-2'] })).toEqual(['acct-1', 'acct-2']);
  });

  it('filters by nothing when neither is present', () => {
    expect(accountFilterFromQuery({})).toEqual([]);
    expect(accountFilterFromQuery({ account: '', accountId: '' })).toEqual([]);
  });
});

// The page writes the selection back to the address under both names. The write
// check on the Auto Optimize tab and the Resolutions tab's own filter read the
// single-account one, so it has to follow what is on screen.
describe('filtersToUrlQuery', () => {
  const empty = { severity: [], account: [], category: [], search: '', safety: [], rules: [], status: [], savings: '', lastSeen: '' };

  it('writes the selected account under both names', () => {
    expect(filtersToUrlQuery({ ...empty, account: ['acct-1'] })).toEqual({ accountId: 'acct-1', account: ['acct-1'] });
  });

  it('keeps a single-account name pointing at one of the selected accounts', () => {
    // Dropping it here would take the Auto Optimize create button away from a
    // user scoped to those accounts.
    expect(filtersToUrlQuery({ ...empty, account: ['acct-1', 'acct-2'] })).toEqual({
      accountId: 'acct-1',
      account: ['acct-1', 'acct-2'],
    });
  });

  it('writes no account at all once the filter is cleared', () => {
    // A leftover account would re-filter the list on the next load, undoing the
    // clear the reader just asked for.
    expect(filtersToUrlQuery(empty)).toEqual({});
  });

  it('carries the other filters through', () => {
    expect(
      filtersToUrlQuery({ ...empty, severity: ['critical'], category: ['RightSizing'], search: 'api', savings: '100-500', lastSeen: '7d' })
    ).toEqual({
      severity: ['critical'],
      category: ['RightSizing'],
      search: 'api',
      savings: '100-500',
      seen: '7d',
    });
  });
});
