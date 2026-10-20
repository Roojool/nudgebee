import { getSessionAccountIds, hasAnyCustomGrant, isTenantWideRole } from '@lib/auth';
import { accessibleAccountOptions, listensToSeveralAccounts, triggerListenAccounts } from '../listenAccounts';

jest.mock('@lib/auth', () => ({
  getSessionAccountIds: jest.fn(),
  hasAnyCustomGrant: jest.fn(),
  isTenantWideRole: jest.fn(),
}));

const trigger = (type: string, params: Record<string, any>) => ({ type: 'trigger', data: { trigger: { type, params } } });

describe('triggerListenAccounts', () => {
  it('reads accounts on event triggers and clusters on optimization triggers', () => {
    expect(triggerListenAccounts({ type: 'event', params: { accounts: ['a', 'b'] } })).toEqual(['a', 'b']);
    expect(triggerListenAccounts({ type: 'optimization', params: { clusters: ['c'] } })).toEqual(['c']);
  });

  it('drops blanks and duplicates, and is empty for other trigger types', () => {
    expect(triggerListenAccounts({ type: 'event', params: { accounts: ['a', '', 'a', 7] } })).toEqual(['a']);
    expect(triggerListenAccounts({ type: 'schedule', params: { accounts: ['a'] } })).toEqual([]);
    expect(triggerListenAccounts(undefined)).toEqual([]);
  });
});

describe('listensToSeveralAccounts', () => {
  it('is true only when a trigger names more than one account', () => {
    expect(listensToSeveralAccounts([trigger('event', { accounts: ['a', 'b'] })])).toBe(true);
    expect(listensToSeveralAccounts([trigger('optimization', { clusters: ['a', 'b'] })])).toBe(true);
    expect(listensToSeveralAccounts([trigger('event', { accounts: ['a'] }), { type: 'action', data: {} }])).toBe(false);
    expect(listensToSeveralAccounts([trigger('event', { accounts: ['a', 'a'] })])).toBe(false);
  });
});

describe('accessibleAccountOptions', () => {
  const options = [{ value: 'a' }, { value: 'b' }, { value: 'c' }];

  beforeEach(() => {
    (isTenantWideRole as jest.Mock).mockReturnValue(false);
    (hasAnyCustomGrant as jest.Mock).mockReturnValue(false);
    (getSessionAccountIds as jest.Mock).mockReturnValue(['a', 'c']);
  });

  it('keeps only the accounts the session grants', () => {
    expect(accessibleAccountOptions(options)).toEqual([{ value: 'a' }, { value: 'c' }]);
  });

  it('keeps every account for tenant-wide and custom-grant users', () => {
    (isTenantWideRole as jest.Mock).mockReturnValue(true);
    expect(accessibleAccountOptions(options)).toEqual(options);
    (isTenantWideRole as jest.Mock).mockReturnValue(false);
    (hasAnyCustomGrant as jest.Mock).mockReturnValue(true);
    expect(accessibleAccountOptions(options)).toEqual(options);
  });
});
