import type { EventRuleCapabilities } from '@api1/kubernetes1';
import { getBrandTitle } from '@hooks/useTenantBranding';

// Alert Manager row controls, shared by the Kubernetes and cloud account lists.
// A control is offered only when the backend reports that the change reaches the
// system that evaluates the rule (eventrules_get_capabilities).

export type AlertRuleItem = {
  id: string;
  alert: string;
  enabled: boolean;
  capabilities?: EventRuleCapabilities;
  [key: string]: any;
};

export type AlertRuleMenuItem = { id: string; label: string; disabled?: boolean };

export const ALERT_RULE_MENU = {
  toggle: 'toggle-enabled',
  edit: 'edit',
  mute: 'mute',
  unmute: 'unmute',
  none: 'no-changes',
} as const;

// "Mute in <product>" when branding has loaded, plain "Mute" until then.
export const muteInBrandLabel = (): string => {
  const title = getBrandTitle();
  return title ? `Mute in ${title}` : 'Mute';
};

// Muted as a whole, or muted for some workloads from the investigation screen.
const hasAnyMute = (caps?: EventRuleCapabilities): boolean => !!caps?.muted || (caps?.muted_workloads ?? 0) > 0;

export const pluralize = (n: number, one: string, many = `${one}s`): string => `${n} ${n === 1 ? one : many}`;

export const buildAlertRuleMenuItems = (item: AlertRuleItem | undefined, canWrite: boolean): AlertRuleMenuItem[] => {
  const caps = item?.capabilities;
  // Until capabilities arrive (or if they failed) show nothing rather than a control
  // that may only change Nudgebee's copy.
  if (!canWrite || !item || !caps) {
    return [];
  }
  const ops = caps.operations;
  const menus: AlertRuleMenuItem[] = [];
  if (ops?.disable_at_source?.available) {
    menus.push({ id: ALERT_RULE_MENU.toggle, label: item.enabled ? 'Disable' : 'Enable' });
  }
  if (item.enabled && ops?.edit_definition?.available) {
    menus.push({ id: ALERT_RULE_MENU.edit, label: 'Edit' });
  }
  // Unmute is offered for any active mute, including a workload mute of a rule
  // whose own Mute is not offered (datadog, built-in checks).
  if (hasAnyMute(caps)) {
    menus.push({ id: ALERT_RULE_MENU.unmute, label: 'Unmute' });
  }
  if (!caps.muted && ops?.mute?.available) {
    menus.push({ id: ALERT_RULE_MENU.mute, label: muteInBrandLabel() });
  }
  if (!menus.length) {
    menus.push({ id: ALERT_RULE_MENU.none, label: 'Change this rule where it is defined', disabled: true });
  }
  return menus;
};

export const alertRuleStatusText = (item: AlertRuleItem | undefined): string => {
  const status = item?.enabled ? 'Enabled' : 'Disabled';
  const caps = item?.capabilities;
  if (caps?.muted) {
    return `${status} · Muted`;
  }
  const workloads = caps?.muted_workloads ?? 0;
  return workloads > 0 ? `${status} · Muted for ${pluralize(workloads, 'workload')}` : status;
};

// Dialogs build their title while closed too, so an absent item gives an empty title.
export const toggleConfirmTitle = (item: AlertRuleItem | undefined): string =>
  item ? `${item.enabled ? 'Disable' : 'Enable'} the alert "${item.alert}" where it is evaluated` : '';

export const muteConfirmTitle = (item: AlertRuleItem | undefined, mute: boolean): string => {
  if (!item) {
    return '';
  }
  if (mute) {
    const title = getBrandTitle();
    return `Mute "${item.alert}"${
      title ? ` in ${title}` : ''
    }? New events are kept but marked Suppressed and send no notifications. The rule itself is not changed.`;
  }
  const workloads = item.capabilities?.muted_workloads ?? 0;
  return workloads > 0
    ? `Unmute "${item.alert}" everywhere, including ${pluralize(
        workloads,
        'workload'
      )} muted on their own? New events will be handled normally again.`
    : `Unmute "${item.alert}"? New events will be handled normally again.`;
};

// A caution the backend attached to an available change (e.g. "the next helm
// upgrade reverts it"). Shown next to the confirm / edit controls.
export const alertRuleWarning = (item: AlertRuleItem | undefined, op: 'disable_at_source' | 'edit_definition'): string | undefined => {
  const cap = item?.capabilities?.operations?.[op];
  return cap?.available ? cap.warning || undefined : undefined;
};
