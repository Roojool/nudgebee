import type { EventAlertControls } from '@api1/kubernetes1';
import {
  alertRuleApplyLabel,
  alertRuleChangeWarning,
  alertRuleMuteConfirm,
  buildAlertRuleContext,
  isAlertRuleChangeApplied,
} from '@components/k8s/investigate/cards/remediationAlertRule';

const cap = (available: boolean, warning?: string) => ({ available, reason: available ? undefined : 'no', warning });

const controls = (over: Partial<EventAlertControls> = {}): EventAlertControls => ({
  event_id: 'e',
  alert: 'OtelDemoGRPCClientErrorRate',
  source: 'pagerduty_webhook',
  rule: {
    rule_id: 'r-1',
    alert: 'OtelDemoGRPCClientErrorRate',
    source: 'prometheus',
    target: 'prom_cr',
    enabled: true,
    muted: false,
    operations: {
      edit_definition: cap(true, 'managed by Helm'),
      update_threshold: cap(true),
      update_duration: cap(true),
      disable_at_source: cap(true),
      mute: cap(true),
      pr: cap(false),
    },
  },
  rule_location: { namespace: 'victoria', name: 'otel-demo-alerts', managed_by: 'unmanaged' },
  rule_definition: { expr: 'rate(x[5m]) > 1', duration: '1m', expr_normalized: 'rate(x[5m]) > 1' },
  scopes: [
    { scope: 'workload', namespace: 'demo', owner: 'fraud-detection', matched_7d: 409, workloads_7d: 1, muted: false },
    { scope: 'rule', matched_7d: 804, workloads_7d: 2, muted: false },
  ],
  suppressions: [],
  ...over,
});

describe('buildAlertRuleContext', () => {
  it('tells the plan the rule, its definition and exactly what may change', () => {
    const built = buildAlertRuleContext(controls(), 'Acme');
    expect(built).not.toBeNull();
    expect(built!.text).toContain('## Alert rule this event came from');
    expect(built!.text).toContain('PrometheusRule victoria/otel-demo-alerts');
    expect(built!.text).toContain('rate(x[5m]) > 1');
    expect(built!.text).toContain('Current for: 1m');
    expect(built!.text).toContain('(managed by Helm)');
    expect(built!.text).toContain('not a prepared fix');
    expect(built!.alertRule).toEqual({
      rule_id: 'r-1',
      alert: 'OtelDemoGRPCClientErrorRate',
      allowed_operations: ['edit', 'disable', 'mute'],
      mute_scopes: ['workload', 'rule'],
      current_expr: 'rate(x[5m]) > 1',
      current_duration: '1m',
    });
  });

  it('offers only what the rule allows', () => {
    // No editable definition (cloud rule, MetricsQL), already disabled, one scope already muted.
    const c = controls({
      rule_definition: undefined,
      rule: { ...controls().rule!, enabled: false },
      scopes: [{ scope: 'workload', namespace: 'demo', owner: 'ad', matched_7d: 3, workloads_7d: 1, muted: true }, controls().scopes[1]],
    });
    const built = buildAlertRuleContext(c, '');
    expect(built!.alertRule.allowed_operations).toEqual(['mute']);
    expect(built!.alertRule.mute_scopes).toEqual(['rule']);
    expect(built!.text).not.toContain('Current expression');
  });

  it('returns nothing when no change is possible', () => {
    expect(buildAlertRuleContext(null, '')).toBeNull();
    expect(
      buildAlertRuleContext(controls({ rule: null, rule_definition: undefined, scopes: [{ ...controls().scopes[1], muted: true }] }), '')
    ).toBeNull();
  });
});

describe('isAlertRuleChangeApplied', () => {
  const edit = { operation: 'edit' as const, expr: 'rate(x[5m]) > 2', expr_normalized: 'rate(x[5m]) > 2', rule_id: 'r-1' };

  it('reads the rule as it is now, in canonical form', () => {
    expect(isAlertRuleChangeApplied(edit, controls())).toBe(false);
    const after = controls({ rule_definition: { expr: 'rate( x[5m] ) > 2', duration: '1m', expr_normalized: 'rate(x[5m]) > 2' } });
    expect(isAlertRuleChangeApplied(edit, after)).toBe(true);
  });

  it('checks the for when that is what changed', () => {
    const forChange = { operation: 'edit' as const, duration: '10m', rule_id: 'r-1' };
    expect(isAlertRuleChangeApplied(forChange, controls())).toBe(false);
    expect(isAlertRuleChangeApplied(forChange, controls({ rule_definition: { expr: 'x', duration: '10m', expr_normalized: 'x' } }))).toBe(true);
  });

  it('turn off and mute follow the rule and the mute state', () => {
    expect(isAlertRuleChangeApplied({ operation: 'disable' }, controls())).toBe(false);
    expect(isAlertRuleChangeApplied({ operation: 'disable' }, controls({ rule: { ...controls().rule!, enabled: false } }))).toBe(true);
    const mute = { operation: 'mute' as const, mute_scope: 'workload' as const };
    expect(isAlertRuleChangeApplied(mute, controls())).toBe(false);
    expect(isAlertRuleChangeApplied(mute, controls({ scopes: [{ ...controls().scopes[0], muted: true }] }))).toBe(true);
    expect(isAlertRuleChangeApplied(mute, null)).toBe(false);
  });
});

describe('card copy', () => {
  it('names the button by what it does and carries the backend caution', () => {
    expect(alertRuleApplyLabel({ operation: 'edit' })).toBe('Apply');
    expect(alertRuleApplyLabel({ operation: 'disable' })).toBe('Turn off');
    expect(alertRuleApplyLabel({ operation: 'mute' })).toBe('Mute');
    expect(alertRuleChangeWarning({ operation: 'edit' }, controls())).toBe('managed by Helm');
    expect(alertRuleChangeWarning({ operation: 'mute' }, controls())).toBeUndefined();
  });
});

describe('alertRuleMuteConfirm', () => {
  const mute = (mute_scope: string) => ({ operation: 'mute', mute_scope, rule_id: 'r-1' });

  it('names the one workload a workload mute covers, and that real failures go quiet too', () => {
    const c = alertRuleMuteConfirm(mute('workload'), controls(), 7);
    expect(c.title).toContain('on demo/fraud-detection only');
    expect(c.text).toContain('For 7 days');
    expect(c.text).toContain('demo/fraud-detection');
    expect(c.text).toContain('including real failures');
  });

  it('never describes a workload mute as covering every workload before its scope loads', () => {
    const c = alertRuleMuteConfirm(mute('workload'), null, 7);
    expect(c.title).toContain('on this workload only');
    expect(c.text).not.toContain('every workload');
    expect(c.text).not.toContain('alert alerts');
    expect(c.text).toContain('For 7 days, alerts for this workload');
  });

  it('says a rule mute covers every workload', () => {
    const c = alertRuleMuteConfirm(mute('rule'), controls(), 7);
    expect(c.title).toContain('everywhere');
    expect(c.text).toContain('every workload');
  });
});
