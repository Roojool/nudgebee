import type { EventRuleCapabilities } from '@api1/kubernetes1';

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
  if (caps.muted) {
    menus.push({ id: ALERT_RULE_MENU.unmute, label: 'Unmute' });
  } else if (ops?.mute?.available) {
    menus.push({ id: ALERT_RULE_MENU.mute, label: 'Mute in Nudgebee' });
  }
  if (!menus.length) {
    menus.push({ id: ALERT_RULE_MENU.none, label: 'Change this rule where it is defined', disabled: true });
  }
  return menus;
};

export const alertRuleStatusText = (item: AlertRuleItem | undefined): string => {
  const status = item?.enabled ? 'Enabled' : 'Disabled';
  return item?.capabilities?.muted ? `${status} · Muted` : status;
};

// Dialogs build their title while closed too, so an absent item gives an empty title.
export const toggleConfirmTitle = (item: AlertRuleItem | undefined): string =>
  item ? `${item.enabled ? 'Disable' : 'Enable'} the alert "${item.alert}" where it is evaluated` : '';

export const muteConfirmTitle = (item: AlertRuleItem | undefined, mute: boolean): string => {
  if (!item) {
    return '';
  }
  return mute
    ? `Mute "${item.alert}" in Nudgebee? New events are kept but marked Suppressed and send no notifications. The rule itself is not changed.`
    : `Unmute "${item.alert}"? New events will be handled normally again.`;
};
