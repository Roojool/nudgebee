import type { EventAlertControls, EventMuteScope } from '@api1/kubernetes1';
import {
  alertDisplayName,
  canDisableAtSource,
  canEditRule,
  hasEventAlertControls,
  mutableScopes,
  mutedScopeText,
  otherSuppressions,
  ruleLocationText,
  scopeImpactText,
  scopeOptionLabel,
} from '@components/k8s/investigate/eventAlertControls';

const cap = (available: boolean, warning?: string) => ({ available, reason: available ? undefined : 'no', warning });

const workload: EventMuteScope = {
  scope: 'workload',
  namespace: 'benchmark-scenarios',
  owner: 'payment-worker',
  matched_7d: 177,
  workloads_7d: 1,
  muted: false,
};
const everywhere: EventMuteScope = { scope: 'rule', matched_7d: 518, workloads_7d: 38, muted: false };

const controls = (over: Partial<EventAlertControls> = {}): EventAlertControls => ({
  event_id: 'e',
  alert: 'report_crash_loop',
  source: 'kubernetes_api_server',
  rule: null,
  scopes: [workload, everywhere],
  suppressions: [],
  ...over,
});

const promRule = (enabled = true, ops: { disable?: boolean; edit?: boolean } = {}) => ({
  rule_id: 'r',
  alert: 'OtelDemoGRPCClientErrorRate',
  source: 'prometheus',
  target: 'prom_cr',
  enabled,
  muted: false,
  operations: {
    edit_definition: cap(ops.edit ?? true),
    update_threshold: cap(true),
    update_duration: cap(true),
    disable_at_source: cap(ops.disable ?? true, 'managed by Helm'),
    mute: cap(true),
    pr: cap(false),
  },
});

describe('scope copy', () => {
  it('says how much each scope silences', () => {
    expect(scopeOptionLabel(workload, 'report_crash_loop')).toBe('Only benchmark-scenarios/payment-worker');
    expect(scopeOptionLabel(everywhere, 'report_crash_loop')).toBe('Every Report Crash Loop alert');
    expect(scopeImpactText(workload)).toBe('177 in the last 7 days');
    // The whole-alert scope names its blast radius in workloads.
    expect(scopeImpactText(everywhere)).toBe('518 in the last 7 days · 38 workloads');
    expect(scopeImpactText({ ...everywhere, workloads_7d: 1 })).toBe('518 in the last 7 days');
    expect(mutedScopeText(workload)).toBe('Muted for benchmark-scenarios/payment-worker');
    expect(mutedScopeText(everywhere)).toBe('Muted for every workload');
  });

  it('keeps alert names that are not snake_case', () => {
    expect(alertDisplayName('OtelDemoGRPCClientErrorRate')).toBe('OtelDemoGRPCClientErrorRate');
  });
});

describe('ruleLocationText', () => {
  it('names the PrometheusRule and who manages it', () => {
    const c = controls({
      rule: promRule() as any,
      rule_location: { namespace: 'ingress-nginx', name: 'ingress-nginx-controller', managed_by: 'helm' },
    });
    expect(ruleLocationText(c, 'Acme')).toBe('PrometheusRule ingress-nginx/ingress-nginx-controller · managed by Helm');
    const unmanaged = controls({
      rule: promRule() as any,
      rule_location: { namespace: 'victoria', name: 'otel-demo-alerts', managed_by: 'unmanaged' },
    });
    expect(ruleLocationText(unmanaged, 'Acme')).toBe('PrometheusRule victoria/otel-demo-alerts');
  });

  it('names cloud rules, built-in checks and webhook copies', () => {
    expect(ruleLocationText(controls({ rule: { ...promRule(), target: 'gcp' } as any }), 'Acme')).toBe('GCP alert policy');
    expect(ruleLocationText(controls(), 'Acme')).toBe('Built-in Acme check');
    expect(ruleLocationText(controls(), '')).toBe('Built-in check');
    expect(ruleLocationText(controls({ source: 'datadog_webhook' }), 'Acme')).toBe('Sent by Datadog');
  });
});

describe('what the section offers', () => {
  it('offers only scopes that are not already muted', () => {
    const c = controls({ scopes: [{ ...workload, muted: true }, everywhere] });
    expect(mutableScopes(c).map((s) => s.scope)).toEqual(['rule']);
  });

  it('offers disable and edit only when the backend says they reach the rule', () => {
    expect(canDisableAtSource(controls())).toBe(false);
    expect(canDisableAtSource(controls({ rule: promRule() as any }))).toBe(true);
    expect(canDisableAtSource(controls({ rule: promRule(true, { disable: false }) as any }))).toBe(false);
    expect(canEditRule(controls({ rule: promRule() as any }))).toBe(true);
    // A disabled rule is re-enabled first, as in Alert Manager.
    expect(canEditRule(controls({ rule: promRule(false) as any }))).toBe(false);
  });

  it('lists suppressions that are not mutes so Unmute never looks like it failed', () => {
    const c = controls({
      suppressions: [
        { rule_id: 'm', name: 'Muted', is_mute: true, mute_scope: 'rule' },
        { rule_id: 'fp', name: 'False positive: X', is_mute: false },
      ],
    });
    expect(otherSuppressions(c).map((s) => s.rule_id)).toEqual(['fp']);
  });

  it('shows the section to read-only users only when something is muted or suppressed', () => {
    expect(hasEventAlertControls(null, true)).toBe(false);
    expect(hasEventAlertControls(controls(), true)).toBe(true);
    expect(hasEventAlertControls(controls(), false)).toBe(false);
    expect(hasEventAlertControls(controls({ scopes: [{ ...workload, muted: true }] }), false)).toBe(true);
    // Nothing to mute (no alert key) and nothing to change: no section.
    expect(hasEventAlertControls(controls({ scopes: [] }), true)).toBe(false);
  });
});
