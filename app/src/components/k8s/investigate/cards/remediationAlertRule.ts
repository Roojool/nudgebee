import type { EventAlertControls } from '@api1/kubernetes1';
import { alertDisplayName, ruleLocationText, scopeImpactText } from '@components/k8s/investigate/eventAlertControls';

// The alert-rule part of Nubi's remediation plan (docs/nubi-alert-rule-fix-spec.md):
// what the plan is told about the event's alert rule, and how a proposed rule change
// is shown and recognised as applied. Kept out of the panel so it is unit-tested.

export type AlertRuleChange = {
  operation: 'edit' | 'disable' | 'mute';
  expr?: string;
  duration?: string;
  mute_scope?: 'workload' | 'rule';
  rule_id?: string;
  expected_expr?: string;
  expected_duration?: string;
  expr_normalized?: string;
};

// What the server checks the plan against: the rule and the changes it allows.
export type RemediationAlertRule = {
  rule_id: string;
  alert: string;
  allowed_operations: string[];
  mute_scopes: string[];
  current_expr: string;
  current_duration: string;
};

// buildAlertRuleContext turns the event's alert controls into the plan's context
// section and the payload the server validates proposals against. null when there is
// nothing the plan could change.
export const buildAlertRuleContext = (
  controls: EventAlertControls | null,
  brand: string
): { text: string; alertRule: RemediationAlertRule } | null => {
  if (!controls?.alert) return null;
  const rule = controls.rule;
  const def = controls.rule_definition;
  const muteScopes = (controls.scopes || []).filter((s) => !s.muted);
  const allowed: string[] = [];
  if (def) allowed.push('edit');
  if (rule?.enabled && rule?.operations?.disable_at_source?.available) allowed.push('disable');
  if (muteScopes.length) allowed.push('mute');
  if (!allowed.length) return null;

  const lines = ['## Alert rule this event came from'];
  lines.push(
    'Context, not a prepared fix. Propose a change to this rule only if your hypotheses conclude the rule itself is wrong and there is no real fault; put it in "alert_rule_change".'
  );
  lines.push(`Rule: ${controls.alert} — ${ruleLocationText(controls, brand)}`);
  if (def) {
    lines.push('Current expression:');
    lines.push('```');
    lines.push(def.expr);
    lines.push('```');
    lines.push(`Current for: ${def.duration || 'not set'}`);
  }
  lines.push('Changes that can be made:');
  if (def) {
    const warning = rule?.operations?.edit_definition?.warning;
    lines.push(`- edit: replace the expression and/or for${warning ? ` (${warning})` : ''}`);
  }
  if (allowed.includes('disable')) {
    const warning = rule?.operations?.disable_at_source?.warning;
    lines.push(`- disable: turn the rule off where it is defined, for everyone${warning ? ` (${warning})` : ''}`);
  }
  for (const s of muteScopes) {
    const who = s.scope === 'workload' ? `only ${s.namespace}/${s.owner}` : 'every workload';
    lines.push(`- mute, scope "${s.scope}": new alerts for ${who} are recorded but notify no one (${scopeImpactText(s)})`);
  }

  return {
    text: lines.join('\n'),
    alertRule: {
      rule_id: rule?.rule_id || '',
      alert: controls.alert,
      allowed_operations: allowed,
      mute_scopes: muteScopes.map((s) => s.scope),
      current_expr: def?.expr || '',
      current_duration: def?.duration || '',
    },
  };
};

// isAlertRuleChangeApplied reads the rule's live state: the badge reflects the
// cluster, not a record, and survives a sync that re-serialises the expression
// (both sides are compared in canonical form).
export const isAlertRuleChangeApplied = (change: AlertRuleChange | undefined, controls: EventAlertControls | null): boolean => {
  if (!change || !controls) return false;
  switch (change.operation) {
    case 'edit': {
      const def = controls.rule_definition;
      if (!def) return false;
      if (change.expr && def.expr_normalized !== change.expr_normalized) return false;
      if (change.duration && def.duration !== change.duration) return false;
      return !!(change.expr || change.duration);
    }
    case 'disable':
      return controls.rule?.enabled === false;
    case 'mute':
      return (controls.scopes || []).some((s) => s.scope === change.mute_scope && s.muted);
    default:
      return false;
  }
};

// alertRuleChangeWarning is the caution the backend attached to this kind of change
// (e.g. "the next helm upgrade reverts it").
export const alertRuleChangeWarning = (change: AlertRuleChange | undefined, controls: EventAlertControls | null): string | undefined => {
  const ops = controls?.rule?.operations;
  if (!change || !ops) return undefined;
  if (change.operation === 'edit') return ops.edit_definition?.warning || undefined;
  if (change.operation === 'disable') return ops.disable_at_source?.warning || undefined;
  return undefined;
};

// alertRuleApplyLabel is the card's button: what the click does.
export const alertRuleApplyLabel = (change: AlertRuleChange): string => {
  if (change.operation === 'disable') return 'Turn off';
  if (change.operation === 'mute') return 'Mute';
  return 'Apply';
};

// alertRuleChangeSummary is one line on what the change does, for disable and mute
// (an edit shows its before/after instead).
export const alertRuleChangeSummary = (change: AlertRuleChange, controls: EventAlertControls | null): string => {
  const name = alertDisplayName(controls?.alert || '');
  if (change.operation === 'disable') return `Turns ${name || 'the rule'} off where it is defined, for everyone.`;
  if (change.operation === 'mute') {
    return `New ${alertsLabel(name)} for ${muteTarget(change, controls)} are still recorded but notify no one.`;
  }
  return '';
};

// alertsLabel is "<name> alerts", or "alerts" before the name has loaded.
const alertsLabel = (name: string): string => (name ? `${name} alerts` : 'alerts');

// muteTarget names what a plan mute covers: one workload, or every workload. A
// workload mute whose scope has not loaded yet is still one workload.
const muteTarget = (change: AlertRuleChange, controls: EventAlertControls | null): string => {
  if (change.mute_scope !== 'workload') return 'every workload';
  const scope = (controls?.scopes || []).find((s) => s.scope === 'workload');
  return scope?.namespace && scope?.owner ? `${scope.namespace}/${scope.owner}` : 'this workload';
};

// alertRuleMuteConfirm is the title and body of the confirmation for a plan mute. It
// names what is muted and says that real failures there go quiet too: a mute cannot
// tell a harmless firing from a real one.
export const alertRuleMuteConfirm = (change: AlertRuleChange, controls: EventAlertControls | null, days: number): { title: string; text: string } => {
  const name = alertDisplayName(controls?.alert || '');
  const target = muteTarget(change, controls);
  const where = target === 'every workload' ? 'everywhere' : `on ${target} only`;
  const period = days === 1 ? '1 day' : `${days} days`;
  return {
    title: `Mute ${name || 'this alert'} ${where}`,
    text: `For ${period}, ${alertsLabel(
      name
    )} for ${target} are still recorded but notify no one — including real failures, not only the harmless ones.`,
  };
};
