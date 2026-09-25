// A rule covers every account or a list of them. The list filter finds a rule by
// any of its accounts, a save sends the whole list, and a failed load is
// reported rather than read as "no rules".

const mockQueryGraphQL = jest.fn();
jest.mock('@lib/HttpService', () => ({
  queryGraphQL: (...args: any[]) => mockQueryGraphQL(...args),
  gqlStringify: (v: any) => JSON.stringify(v),
}));
jest.mock('@api1/integrations', () => ({ __esModule: true, default: { listIntegrations: jest.fn() } }));

import { ruleAccountIds } from '@components/notifications/ruleAccounts';

const api = require('@api1/notification').default;

const rows = { data: { data: { notifications_list_rules: { rows: [] }, notifications_aggregate_rules: { rows: [{ count: 0 }] } } } };

describe('notification rule accounts', () => {
  beforeEach(() => mockQueryGraphQL.mockReset());

  it("filters the list by any of a rule's accounts", async () => {
    mockQueryGraphQL.mockResolvedValue(rows);
    await api.getNotificationRules({ accountId: 'acc-1' }, 10, 0);

    const query: string = mockQueryGraphQL.mock.calls[0][0];
    expect(query).toContain(JSON.stringify({ account_ids: { _contains: '["acc-1"]' } }));
    expect(query).not.toContain('"account_id"');
  });

  it('reports a failed load instead of an empty list', async () => {
    mockQueryGraphQL.mockResolvedValue({ data: { errors: [{ message: 'unknown column account_ids' }] } });
    await expect(api.getNotificationRules({}, 10, 0)).resolves.toEqual({ error: 'unknown column account_ids' });

    mockQueryGraphQL.mockRejectedValue(new Error('network down'));
    await expect(api.getNotificationRules({}, 10, 0)).resolves.toEqual({ error: 'Failed to load notification rules' });
  });

  it('saves every selected account', async () => {
    mockQueryGraphQL.mockResolvedValue({ data: { data: { notifications_upsert_rule: { id: 'rule-1' } } } });
    await api.insertNotificationRule({ ruleName: 'Prod alerts', source: 'troubleshoot', isSuppressed: true, accountIds: ['acc-1', 'acc-2'] });

    const { object } = mockQueryGraphQL.mock.calls[0][2];
    expect(object.account_ids).toEqual(['acc-1', 'acc-2']);
    expect(object).not.toHaveProperty('account_id');
    expect(object).not.toHaveProperty('cluster');
  });

  it('sends no accounts for a rule without an account scope', async () => {
    mockQueryGraphQL.mockResolvedValue({ data: { data: { notifications_upsert_rule: { id: 'rule-1' } } } });
    await api.insertNotificationRule({ ruleName: 'Daily recap', source: 'daily_recap', isSuppressed: false });

    expect(mockQueryGraphQL.mock.calls[0][2].object).not.toHaveProperty('account_ids');
  });
});

describe('ruleAccountIds', () => {
  it('reads the encoded list the rules list returns, or an array', () => {
    expect(ruleAccountIds({ account_ids: '["acc-1","acc-2"]' })).toEqual(['acc-1', 'acc-2']);
    expect(ruleAccountIds({ account_ids: ['acc-1'] })).toEqual(['acc-1']);
  });

  it('is empty for a rule for every account or a malformed value', () => {
    for (const value of ['[]', null, undefined, 'not json', '{"a":1}']) {
      expect(ruleAccountIds({ account_ids: value })).toEqual([]);
    }
    expect(ruleAccountIds(undefined)).toEqual([]);
    expect(ruleAccountIds({ account_ids: '["acc-1", 2]' })).toEqual(['acc-1']);
  });
});
