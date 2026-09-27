import {
  cleanRuleName,
  covers,
  isValidRuleName,
  newRule,
  parseUtc,
  ruleFromRow,
  ruleIssues,
  ruleStatus,
  suggestedName,
  toUpsertInput,
  whatText,
  whoText,
} from '@components/notifications/rules/model';

jest.mock('@assets', () => ({}));

const name = (id: string) => ({ a1: 'k8s-prod', a2: 'k8s-dev', a3: 'k8s-qa' }[id] || id);
const HOUR = 3600e3;

describe('ruleFromRow', () => {
  it('reads the list row, json columns arriving as strings', () => {
    const rule = ruleFromRow({
      id: 'r1',
      name: 'Prod alerts',
      source: 'troubleshoot',
      source_mode: 'list',
      sources: '["troubleshoot","slo"]',
      scope_mode: 'all',
      account_ids: '[]',
      exceptions: '[{"account_ids":["a2"]},{"namespaces":["kube-system"]}]',
      is_suppressed: false,
      is_active: true,
      expires_at: '2026-10-01T10:00:00',
      notification_rule_mappings: '[{"platform":"slack","channels":{"id":"C1","name":"prod"}}]',
    });
    expect(rule.types).toEqual(['troubleshoot', 'slo']);
    expect(rule.scope).toBe('all');
    expect(rule.except).toEqual(['a2']);
    expect(rule.otherExceptions).toEqual([{ namespaces: ['kube-system'] }]);
    expect(rule.mappings).toEqual([{ platform: 'slack', channels: { id: 'C1', name: 'prod' } }]);
    expect(rule.until).toBe('2026-10-01T10:00:00.000Z');
  });

  it('reads a rule for all types, and a rule saved before types existed', () => {
    expect(ruleFromRow({ source: 'troubleshoot', source_mode: 'all', sources: '["troubleshoot"]' }).types).toBe('all');
    expect(ruleFromRow({ source: 'cloud', source_mode: null, sources: '["cloud"]' }).types).toEqual(['cloud']);
  });

  it('reads a switched-off rule as off', () => {
    expect(ruleFromRow({ source: 'optimize', is_active: false }).active).toBe(false);
    expect(ruleStatus(ruleFromRow({ source: 'optimize', is_active: false })).text).toBe('Off');
  });
});

describe('toUpsertInput', () => {
  it('sends types, scope and the accounts left out', () => {
    const input = toUpsertInput(
      newRule({ name: 'x', types: ['troubleshoot', 'slo'], except: ['a2'], mappings: [{ platform: 'slack', channels: { id: 'C1' } }] })
    );
    expect(input).toMatchObject({
      source: 'troubleshoot',
      source_mode: 'list',
      sources: ['troubleshoot', 'slo'],
      scope_mode: 'all',
      account_ids: [],
      exceptions: [{ account_ids: ['a2'] }],
      is_suppressed: false,
      expires_at: null,
    });
    expect(input.event_types).toBeUndefined();
  });

  it('sends a rule for all types with the mirror source and no list', () => {
    const input = toUpsertInput(newRule({ name: 'x', types: 'all', action: 'drop', scope: 'some', accounts: ['a3'] }));
    expect(input).toMatchObject({ source: 'troubleshoot', source_mode: 'all', scope_mode: 'accounts', account_ids: ['a3'], is_suppressed: true });
    expect(input.sources).toBeUndefined();
    // A Don't send keeps whatever channels it had.
    expect(input.mappings).toBeUndefined();
  });

  it('keeps accounts off rules for types that always cover every account', () => {
    const input = toUpsertInput(newRule({ name: 'x', types: ['weekly_digest'], scope: 'some', accounts: ['a1'], except: ['a2'] }));
    expect(input).toMatchObject({ scope_mode: 'all', account_ids: [], exceptions: [] });
  });

  it('names optimization events only when some are left out, and always for automation', () => {
    const all = toUpsertInput(newRule({ name: 'x', types: ['optimize'], events: null }));
    expect(all.event_types).toBeUndefined();
    const some = toUpsertInput(newRule({ name: 'x', types: ['optimize'], events: ['optimize.digest'] }));
    expect(some.event_types).toEqual(['optimize.digest']);
    const automation = toUpsertInput(newRule({ name: 'x', types: ['automation'], events: null }));
    expect(automation.event_types).toEqual(['automation.task.completed', 'automation.task.progress', 'automation.pr.created']);
  });

  it('sends alert, severity and hourly grouping only for troubleshooting alone', () => {
    const ts = toUpsertInput(newRule({ name: 'x', alert: 'HighLatency', severity: 'high', batch: true }));
    expect(ts).toMatchObject({ aggregation_key: 'HighLatency', severity: 'high', delivery_mode: 'batch', frequency: 'hourly' });
    const both = toUpsertInput(newRule({ name: 'x', types: ['troubleshoot', 'slo'], alert: 'HighLatency', severity: 'high', batch: true }));
    expect(both.aggregation_key).toBeUndefined();
    expect(both.delivery_mode).toBeUndefined();
  });

  it('round-trips through the list row', () => {
    const rule = newRule({ id: 'r1', name: 'x', types: ['slo', 'cloud'], except: ['a1'], namespace: 'payments' });
    const input = toUpsertInput(rule);
    const back = ruleFromRow({ ...input, sources: JSON.stringify(input.sources), exceptions: JSON.stringify(input.exceptions) });
    expect(back.types).toEqual(['slo', 'cloud']);
    expect(back.except).toEqual(['a1']);
    // Namespace applies only when every type has namespaces; cloud spend doesn't.
    expect(input.namespace).toBeUndefined();
  });
});

describe('words', () => {
  it('reads a rule as what and from where', () => {
    const rule = newRule({ types: ['optimize'], events: ['optimize.digest', 'optimize.nudge'], scope: 'all', except: ['a2'] });
    expect(whatText(rule)).toBe('Optimization updates (daily recommendation digest and proactive nudges)');
    expect(whoText(rule, name)).toBe('all accounts except k8s-dev');
    expect(whoText(newRule({ types: ['weekly_digest'], scope: 'some', accounts: ['a1'] }), name)).toBe('every account');
  });

  it('suggests names the backend accepts', () => {
    const rule = newRule({ scope: 'some', accounts: ['a1'], mappings: [{ platform: 'slack', channels: { name: 'prod-alerts' } }] });
    const suggested = suggestedName(rule, name);
    expect(suggested).toBe('Troubleshooting alerts from k8s-prod to prod-alerts');
    expect(isValidRuleName(suggested)).toBe(true);
    expect(cleanRuleName('#1 (copy): alerts, k8s.prod')).toBe('copy alerts k8s prod');
  });
});

describe('status', () => {
  const now = Date.parse('2026-09-27T10:00:00Z');
  it('tells Don’t send, a pause, an ended pause and an older snooze apart', () => {
    expect(ruleStatus(newRule({ action: 'drop' }), now).text).toBe('Don’t send');
    expect(ruleStatus(newRule({ action: 'drop', until: new Date(now + 5 * HOUR).toISOString() }), now).text).toBe('Paused · 5h left');
    expect(ruleStatus(newRule({ action: 'drop', until: new Date(now - HOUR).toISOString() }), now).text).toBe('Pause ended');
    expect(ruleStatus(newRule({ action: 'send', until: new Date(now + 3 * 24 * HOUR).toISOString() }), now)).toMatchObject({
      text: 'Paused · 3d left',
      tone: 'warning',
    });
    expect(ruleStatus(newRule({ batch: true }), now).text).toBe('Send hourly');
  });

  it('reads a zone-less timestamp as UTC', () => {
    expect(parseUtc('2026-09-27 10:00:00')).toBe('2026-09-27T10:00:00.000Z');
    expect(parseUtc('2026-09-27T10:00:00+05:30')).toBe('2026-09-27T04:30:00.000Z');
  });
});

describe('coverage and conflicts', () => {
  const ids = ['a1', 'a2', 'a3'];
  const silenceAll = newRule({ id: 's', name: 'Silence all troubleshooting', action: 'drop', except: ['a1'] });
  const devToChannel = newRule({
    id: 'd',
    name: 'Dev alerts',
    scope: 'some',
    accounts: ['a2'],
    mappings: [{ platform: 'slack', channels: { name: 'dev' } }],
  });

  it('matches by type, account, exception and severity', () => {
    expect(covers(silenceAll, 'troubleshoot', { account: 'a2' })).toBe(true);
    expect(covers(silenceAll, 'troubleshoot', { account: 'a1' })).toBe(false);
    expect(covers(silenceAll, 'slo', { account: 'a2' })).toBe(false);
    expect(covers(newRule({ severity: 'high' }), 'troubleshoot', { account: 'a1', severity: 'medium' })).toBe(false);
  });

  it('warns a Send rule that a Don’t send stops all of it, and offers the exception', () => {
    const [issue] = ruleIssues(devToChannel, [silenceAll, devToChannel], ids, name);
    expect(issue.kind).toBe('warning');
    expect(issue.text).toBe('Never sends: “Silence all troubleshooting” stops everything this rule covers.');
    expect(issue.fix).toMatchObject({ kind: 'save', label: 'Let k8s-dev through in “Silence all troubleshooting”' });
    expect((issue.fix as any).rule.except).toEqual(['a1', 'a2']);
  });

  it('ignores a Don’t send whose pause ended, and flags that pause for removal', () => {
    const ended = { ...silenceAll, until: new Date(Date.now() - HOUR).toISOString() };
    expect(ruleIssues(devToChannel, [ended, devToChannel], ids, name)).toEqual([]);
    expect(ruleIssues(ended, [ended], ids, name)[0]).toMatchObject({ kind: 'ended', fix: { kind: 'delete' } });
  });

  it('ignores rules that are off', () => {
    expect(ruleIssues(devToChannel, [{ ...silenceAll, active: false }, devToChannel], ids, name)).toEqual([]);
  });
});
