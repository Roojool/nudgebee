import {
  alertRuleStatusText,
  alertRuleWarning,
  buildAlertRuleMenuItems,
  muteConfirmTitle,
  toggleConfirmTitle,
} from '@components/k8s/details/alertRuleControls';
import { getBrandTitle } from '@hooks/useTenantBranding';

// Copy names the tenant's product, never a hardcoded one.
jest.mock('@hooks/useTenantBranding', () => ({ getBrandTitle: jest.fn(() => 'Acme') }));

const ops = (available: boolean) => ({ available, reason: available ? undefined : 'no' });
const item = (enabled: boolean, over: Record<string, boolean> = {}, muted = false, mutedWorkloads = 0) => ({
  id: 'r',
  alert: 'A',
  enabled,
  capabilities: {
    rule_id: 'r',
    alert: 'A',
    source: 'prometheus',
    target: 'prom_cr',
    muted,
    muted_workloads: mutedWorkloads,
    operations: {
      edit_definition: ops(over.edit ?? true),
      update_threshold: ops(true),
      update_duration: ops(true),
      disable_at_source: ops(over.disable ?? true),
      mute: ops(over.mute ?? true),
      pr: ops(false),
    },
  },
});

const labels = (i: any, canWrite = true) => buildAlertRuleMenuItems(i, canWrite).map((m) => m.label);

describe('buildAlertRuleMenuItems', () => {
  it('offers only what the backend reports as available', () => {
    expect(labels(item(true))).toEqual(['Disable', 'Edit', 'Mute in Acme']);
    expect(labels(item(false))).toEqual(['Enable', 'Mute in Acme']);
    expect(labels(item(true, { disable: false, edit: false }))).toEqual(['Mute in Acme']);
  });

  it('offers unmute for a muted rule even when mute is no longer offered', () => {
    expect(labels(item(true, { mute: false }, true))).toEqual(['Disable', 'Edit', 'Unmute']);
  });

  it('offers unmute for workloads muted on their own, and still offers muting the whole rule', () => {
    expect(labels(item(true, {}, false, 2))).toEqual(['Disable', 'Edit', 'Unmute', 'Mute in Acme']);
    // A rule Nudgebee cannot mute as a whole (datadog, built-in checks) can still carry workload mutes.
    expect(labels(item(true, { disable: false, edit: false, mute: false }, false, 1))).toEqual(['Unmute']);
  });

  it('falls back to a brand-free label until branding loads', () => {
    (getBrandTitle as jest.Mock).mockReturnValueOnce('');
    expect(labels(item(true, { disable: false, edit: false }))).toEqual(['Mute']);
  });

  it('shows a disabled hint when nothing can be changed here', () => {
    const menu = buildAlertRuleMenuItems(item(true, { disable: false, edit: false, mute: false }), true);
    expect(menu).toEqual([{ id: 'no-changes', label: 'Change this rule where it is defined', disabled: true }]);
  });

  it('shows nothing without write access or before capabilities load', () => {
    expect(labels(item(true), false)).toEqual([]);
    expect(labels({ id: 'r', alert: 'A', enabled: true })).toEqual([]);
  });
});

describe('alertRuleStatusText', () => {
  it('adds Muted to the status', () => {
    expect(alertRuleStatusText(item(true, {}, true))).toBe('Enabled · Muted');
    expect(alertRuleStatusText(item(false))).toBe('Disabled');
  });

  it('counts workloads muted on their own', () => {
    expect(alertRuleStatusText(item(true, {}, false, 1))).toBe('Enabled · Muted for 1 workload');
    expect(alertRuleStatusText(item(true, {}, false, 3))).toBe('Enabled · Muted for 3 workloads');
    expect(alertRuleStatusText(item(true, {}, true, 3))).toBe('Enabled · Muted');
  });
});

describe('confirm titles', () => {
  it('are empty while no rule is selected', () => {
    expect(toggleConfirmTitle(undefined)).toBe('');
    expect(muteConfirmTitle(undefined, true)).toBe('');
    expect(toggleConfirmTitle(item(true))).toBe('Disable the alert "A" where it is evaluated');
  });

  it('say that unmute also clears workload mutes', () => {
    expect(muteConfirmTitle(item(true, {}, false, 2), false)).toContain('including 2 workloads muted on their own');
    expect(muteConfirmTitle(item(true), true)).toContain('in Acme?');
  });
});

describe('alertRuleWarning', () => {
  it('returns the warning of an available change only', () => {
    const warned = item(true);
    warned.capabilities.operations.disable_at_source = {
      available: true,
      reason: undefined,
      warning: 'the next helm upgrade reverts the change',
    } as any;
    expect(alertRuleWarning(warned, 'disable_at_source')).toBe('the next helm upgrade reverts the change');
    expect(alertRuleWarning(warned, 'edit_definition')).toBeUndefined();

    const blocked = item(true, { disable: false });
    (blocked.capabilities.operations.disable_at_source as any).warning = 'ignored';
    expect(alertRuleWarning(blocked, 'disable_at_source')).toBeUndefined();
    expect(alertRuleWarning(undefined, 'disable_at_source')).toBeUndefined();
  });
});
