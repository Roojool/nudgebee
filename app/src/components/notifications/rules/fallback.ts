import { NotificationDefaultAction, NotificationDefaultPolicy } from '@api1/notification';
import { FALLBACK_TYPE_KEYS, RuleTypeKey } from './catalogue';
import { RuleMapping } from './model';

export type FallbackMode = 'defaults' | 'channels' | 'drop' | 'inherit';
export type FallbackKey = 'all' | RuleTypeKey;

export interface Fallback {
  mode: FallbackMode;
  mappings: RuleMapping[];
}

export type Fallbacks = Record<FallbackKey, Fallback>;

const MODE_BY_ACTION: Record<string, FallbackMode> = { deliver_defaults: 'defaults', deliver_channels: 'channels', drop: 'drop' };
const ACTION_BY_MODE: Record<FallbackMode, NotificationDefaultAction> = {
  defaults: 'deliver_defaults',
  channels: 'deliver_channels',
  drop: 'drop',
  inherit: 'inherit',
};

// No saved row: all types send to each app's default channel, and a type follows all types.
export const fallbacksFromPolicies = (policies: NotificationDefaultPolicy[]): Fallbacks => {
  const saved = Object.fromEntries(policies.map((p) => [p.source, p]));
  const read = (key: FallbackKey): Fallback => {
    const p = saved[key];
    if (!p) return { mode: key === 'all' ? 'defaults' : 'inherit', mappings: [] };
    return { mode: MODE_BY_ACTION[p.action] || 'defaults', mappings: (p.mappings as RuleMapping[]) || [] };
  };
  return Object.fromEntries((['all', ...FALLBACK_TYPE_KEYS] as FallbackKey[]).map((key) => [key, read(key)])) as Fallbacks;
};

export const fallbackRequest = (key: FallbackKey, fallback: Fallback): NotificationDefaultPolicy => ({
  source: key,
  action: ACTION_BY_MODE[fallback.mode],
  mappings: fallback.mode === 'channels' ? fallback.mappings.map((m) => ({ platform: m.platform, channels: m.channels })) : [],
});

// What a type's notifications no rule covers do, with whether it comes from the all-types setting.
export const effectiveFallback = (fallbacks: Fallbacks, key: RuleTypeKey): Fallback & { inherited: boolean } => {
  const own = fallbacks[key];
  if (!own || own.mode === 'inherit') return { ...fallbacks.all, inherited: true };
  return { ...own, inherited: false };
};

export const sameFallback = (a: Fallback, b: Fallback) => a.mode === b.mode && JSON.stringify(a.mappings) === JSON.stringify(b.mappings);
