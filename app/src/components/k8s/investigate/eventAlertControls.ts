import type { EventAlertControls, EventMuteScope, EventSuppression } from '@api1/kubernetes1';
import { pluralize } from '@components/k8s/details/alertRuleControls';
import { snakeToTitleCase } from 'src/utils/common';

// Copy and decisions for the "Stop this alert" section of the investigation
// screen (docs/alert-rule-controls-on-investigation-spec.md). Kept out of the
// component so the rules are unit-tested.

// Durations a mute from this screen can run for. "Until I unmute" is last so the
// default is a mute that ends by itself.
export const MUTE_DURATION_OPTIONS = [
  { value: '168', label: '7 days' },
  { value: '24', label: '24 hours' },
  { value: '0', label: 'Until I unmute' },
];
export const DEFAULT_MUTE_DURATION = '168';

// Built-in checks are keyed in snake_case (report_crash_loop); show them the way
// the left panel does.
export const alertDisplayName = (alert: string): string => (alert?.includes('_') ? snakeToTitleCase(alert) : alert);

const SOURCE_LABELS: Record<string, string> = {
  datadog_webhook: 'Datadog',
  pagerduty_webhook: 'PagerDuty',
  zenduty_webhook: 'Zenduty',
  elasticsearch_webhook: 'Elasticsearch',
  prometheus_alertmanager_webhook: 'Alertmanager',
  azure_monitor_webhook: 'Azure Monitor',
  anomaly: 'anomaly detection',
  automation: 'an automation',
};

const MANAGED_BY: Record<string, string> = {
  helm: 'managed by Helm',
  gitops: 'managed by GitOps',
  chart: 'from the agent chart',
};

// Where the alert is defined, in words ("PrometheusRule victoria/otel-demo-alerts").
export const ruleLocationText = (c: EventAlertControls, brand: string): string => {
  const target = c.rule?.target;
  if (target === 'prom_cr' && c.rule_location?.name) {
    const where = `PrometheusRule ${c.rule_location.namespace}/${c.rule_location.name}`;
    const managed = MANAGED_BY[c.rule_location.managed_by || ''];
    return managed ? `${where} · ${managed}` : where;
  }
  switch (target) {
    case 'aws':
      return 'CloudWatch alarm';
    case 'gcp':
      return 'GCP alert policy';
    case 'azure':
      return 'Azure Monitor alert';
    case 'prom_ruler':
      return 'Your Prometheus ruler';
  }
  if (c.source === 'kubernetes_api_server') {
    return brand ? `Built-in ${brand} check` : 'Built-in check';
  }
  const label = SOURCE_LABELS[c.source];
  return label ? `Sent by ${label}` : `Sent by ${c.source}`;
};

export const scopeOptionLabel = (s: EventMuteScope, alert: string): string =>
  s.scope === 'workload' ? `Only ${s.namespace}/${s.owner}` : `Every ${alertDisplayName(alert)} alert`;

// How much a scope silences: "518 in the last 7 days · 38 workloads".
export const scopeImpactText = (s: EventMuteScope): string => {
  const matched = `${s.matched_7d} in the last 7 days`;
  return s.scope === 'rule' && s.workloads_7d > 1 ? `${matched} · ${pluralize(s.workloads_7d, 'workload')}` : matched;
};

export const mutedScopeText = (s: EventMuteScope): string =>
  s.scope === 'workload' ? `Muted for ${s.namespace}/${s.owner}` : 'Muted for every workload';

export const mutedScopes = (c: EventAlertControls | null): EventMuteScope[] => (c?.scopes || []).filter((s) => s.muted);

// Suppression rules that are not mutes (e.g. from "classify as false positive").
// They cannot be undone here, but hiding them would make Unmute look like it
// failed while the event stays suppressed.
export const otherSuppressions = (c: EventAlertControls | null): EventSuppression[] => (c?.suppressions || []).filter((s) => !s.is_mute);

// Scopes that can still be muted, narrowest first.
export const mutableScopes = (c: EventAlertControls | null): EventMuteScope[] => (c?.scopes || []).filter((s) => !s.muted);

export const canDisableAtSource = (c: EventAlertControls | null): boolean => !!c?.rule?.operations?.disable_at_source?.available;

export const canEditRule = (c: EventAlertControls | null): boolean => !!c?.rule?.enabled && !!c?.rule?.operations?.edit_definition?.available;

// The section shows when there is something to do, or a suppression to explain.
export const hasEventAlertControls = (c: EventAlertControls | null, canWrite: boolean): boolean => {
  if (!c) {
    return false;
  }
  if (mutedScopes(c).length > 0 || otherSuppressions(c).length > 0) {
    return true;
  }
  return canWrite && (mutableScopes(c).length > 0 || canDisableAtSource(c) || canEditRule(c));
};
