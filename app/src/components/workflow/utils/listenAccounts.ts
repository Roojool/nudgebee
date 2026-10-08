import { getSessionAccountIds, hasAnyCustomGrant, isTenantWideRole } from '@lib/auth';

/** Step `account_id` that targets whichever account the triggering event came from. */
export const EVENT_ACCOUNT_TEMPLATE = '{{ Inputs.event.cloud_account_id }}';

// Mirrors runbook-server model.Trigger.ListenAccounts: event triggers name them in `accounts`,
// optimization triggers in `clusters`. Empty means the workflow's own account.
export const triggerListenAccounts = (trigger?: { type?: string; params?: Record<string, any> } | null): string[] => {
  const key = trigger?.type === 'event' ? 'accounts' : trigger?.type === 'optimization' ? 'clusters' : '';
  const raw = key ? trigger?.params?.[key] : undefined;
  return Array.isArray(raw) ? Array.from(new Set(raw.filter((a): a is string => typeof a === 'string' && a !== ''))) : [];
};

/** True when any trigger in the graph listens to more than one account. */
export const listensToSeveralAccounts = (nodes: { type?: string; data?: any }[]): boolean =>
  nodes.some((n) => n.type === 'trigger' && triggerListenAccounts(n.data?.trigger).length > 1);

// Tenant-wide and custom-grant users reach accounts their session lists don't name, so for them
// the tenant's account list is already the set they can use (same rule as useAccountGuard).
export const accessibleAccountOptions = <T extends { value: string }>(options: T[]): T[] => {
  if (isTenantWideRole() || hasAnyCustomGrant()) return options;
  const allowed = new Set(getSessionAccountIds());
  return options.filter((o) => allowed.has(o.value));
};
