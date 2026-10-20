// getNotificationRuleEventTypes reads event_types from the rules list, which
// returns json columns as encoded strings; the editor needs an array either way.

const mockQueryGraphQL = jest.fn();
jest.mock('@lib/HttpService', () => ({
  queryGraphQL: (...args: any[]) => mockQueryGraphQL(...args),
  gqlStringify: (v: any) => JSON.stringify(v),
}));
jest.mock('@api1/integrations', () => ({ __esModule: true, default: { listIntegrations: jest.fn() } }));

const api = require('@api1/notification').default;

const respond = (eventTypes: any) =>
  mockQueryGraphQL.mockResolvedValue({ data: { data: { notifications_list_rules: { rows: [{ id: 'rule-1', event_types: eventTypes }] } } } });

describe('getNotificationRuleEventTypes', () => {
  beforeEach(() => mockQueryGraphQL.mockReset());

  it('parses a JSON-encoded string, as the rules list returns it', async () => {
    respond('["automation.task.completed", "automation.task.progress"]');
    await expect(api.getNotificationRuleEventTypes('rule-1')).resolves.toEqual(['automation.task.completed', 'automation.task.progress']);
  });

  it('accepts an array as-is', async () => {
    respond(['automation.task.failed']);
    await expect(api.getNotificationRuleEventTypes('rule-1')).resolves.toEqual(['automation.task.failed']);
  });

  it('returns an empty list for a missing or malformed value', async () => {
    for (const value of [null, undefined, 'not json', '{"a":1}']) {
      respond(value);
      await expect(api.getNotificationRuleEventTypes('rule-1')).resolves.toEqual([]);
    }
  });

  it('drops entries that are not strings', async () => {
    respond('["automation.task.completed", 1, null]');
    await expect(api.getNotificationRuleEventTypes('rule-1')).resolves.toEqual(['automation.task.completed']);
  });

  it('scopes the query to the rule id', async () => {
    respond('[]');
    await api.getNotificationRuleEventTypes('rule-9');
    expect(mockQueryGraphQL.mock.calls[0][0]).toContain('"_eq":"rule-9"');
  });
});
