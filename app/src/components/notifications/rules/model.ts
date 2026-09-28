import { safeJSONParse } from 'src/utils/common';
import { ALL_TYPE_KEYS, RULE_TYPE, RuleTypeKey, isRuleTypeKey } from './catalogue';

export interface RuleMapping {
  platform: string;
  channels: any;
  installation_id?: string;
}

export type ExceptionGroup = Record<string, string[]>;

export interface Rule {
  id?: string;
  name: string;
  description: string;
  types: RuleTypeKey[] | 'all';
  // Events of a one-type automation or optimization rule; null means all of them.
  events: string[] | null;
  scope: 'all' | 'some';
  accounts: string[];
  // Accounts a rule for all accounts leaves out.
  except: string[];
  // Exception groups this page doesn't edit (namespaces, workloads, alerts), kept as they are.
  otherExceptions: ExceptionGroup[];
  action: 'send' | 'drop';
  mappings: RuleMapping[];
  namespace: string;
  workload: string;
  alert: string;
  severity: string;
  batch: boolean;
  // Don't send ends then; on a Send rule it is an older snooze that stops it until then.
  until: string | null;
  active: boolean;
}

export const newRule = (patch: Partial<Rule> = {}): Rule => ({
  name: '',
  description: '',
  types: ['troubleshoot'],
  events: null,
  scope: 'all',
  accounts: [],
  except: [],
  otherExceptions: [],
  action: 'send',
  mappings: [],
  namespace: '',
  workload: '',
  alert: '',
  severity: '',
  batch: false,
  until: null,
  active: true,
  ...patch,
});

const parseJson = (value: unknown): any => (typeof value === 'string' ? safeJSONParse(value) : value);

const stringList = (value: unknown): string[] => {
  const parsed = parseJson(value);
  return Array.isArray(parsed) ? parsed.filter((v: unknown): v is string => typeof v === 'string' && v !== '') : [];
};

// The database stores UTC without a zone; read it as UTC.
export const parseUtc = (value: unknown): string | null => {
  if (!value || typeof value !== 'string') return null;
  const iso = /[zZ]|[+-]\d\d:?\d\d$/.test(value) ? value : `${value.replace(' ', 'T')}Z`;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
};

const isTrue = (value: unknown) => value === true || value === 'true';

export const ruleFromRow = (row: any): Rule => {
  let types: Rule['types'];
  if (row.source_mode === 'all') {
    types = 'all';
  } else {
    const listed = stringList(row.sources).filter(isRuleTypeKey);
    types = listed.length ? listed : isRuleTypeKey(row.source) ? [row.source] : ['troubleshoot'];
  }
  const accountIds = stringList(row.account_ids);
  const scope: Rule['scope'] = row.scope_mode === 'accounts' || (!row.scope_mode && accountIds.length) ? 'some' : 'all';
  const groups: ExceptionGroup[] = (parseJson(row.exceptions) || []).filter((g: unknown) => g && typeof g === 'object');
  const accountOnly = (g: ExceptionGroup) => Object.keys(g).length === 1 && Array.isArray(g.account_ids);
  const events = parseJson(row.event_types);
  const mappings = parseJson(row.notification_rule_mappings);
  return {
    id: row.id,
    name: row.name || '',
    description: row.description || '',
    types,
    events: Array.isArray(events) && events.length ? events : null,
    scope,
    accounts: scope === 'some' ? accountIds : [],
    except: Array.from(new Set(groups.filter(accountOnly).flatMap((g) => g.account_ids))),
    otherExceptions: groups.filter((g) => !accountOnly(g)),
    action: isTrue(row.is_suppressed) ? 'drop' : 'send',
    mappings: Array.isArray(mappings) ? mappings.map((m: any) => ({ platform: m.platform, channels: parseJson(m.channels) })) : [],
    namespace: row.namespace || '',
    workload: row.workload || '',
    alert: row.aggregation_key || '',
    severity: row.severity || '',
    batch: row.delivery_mode === 'batch',
    until: parseUtc(row.expires_at),
    active: row.is_active !== false && row.is_active !== 'false',
  };
};

export const typesOf = (rule: Pick<Rule, 'types'>): RuleTypeKey[] => (rule.types === 'all' ? ALL_TYPE_KEYS : rule.types);

// The one type whose events a rule chooses, if it covers exactly one type with events.
export const eventTypeOf = (types: Rule['types']): RuleTypeKey | null =>
  types !== 'all' && types.length === 1 && RULE_TYPE[types[0]].events ? types[0] : null;

export const onlyTroubleshooting = (types: Rule['types']) => types !== 'all' && types.length === 1 && types[0] === 'troubleshoot';

// Namespace and application filters apply when every covered type has them.
export const hasWorkloadFilters = (types: Rule['types']) => typesOf({ types }).every((t) => RULE_TYPE[t].workloads);

// Every covered type always covers every account.
export const coversNoAccounts = (types: Rule['types']) => typesOf({ types }).every((t) => !RULE_TYPE[t].accounts);

export const isEnded = (rule: Pick<Rule, 'until'>, now = Date.now()) => !!rule.until && new Date(rule.until).getTime() <= now;
export const isPaused = (rule: Pick<Rule, 'until'>, now = Date.now()) => !!rule.until && new Date(rule.until).getTime() > now;

export const toUpsertInput = (rule: Rule): Record<string, any> => {
  const types = typesOf(rule);
  const single = rule.types !== 'all' && rule.types.length === 1 ? rule.types[0] : null;
  const noAccounts = coversNoAccounts(rule.types);
  const scopeAll = noAccounts || rule.scope === 'all';
  const exceptions = [...rule.otherExceptions];
  if (scopeAll && !noAccounts && rule.except.length) {
    exceptions.push({ account_ids: rule.except });
  }
  const input: Record<string, any> = {
    name: rule.name,
    source: rule.types === 'all' ? 'troubleshoot' : types[0],
    source_mode: rule.types === 'all' ? 'all' : 'list',
    scope_mode: scopeAll ? 'all' : 'accounts',
    account_ids: scopeAll ? [] : rule.accounts,
    exceptions,
    is_suppressed: rule.action === 'drop',
    expires_at: rule.until,
    workload: null,
  };
  if (rule.types !== 'all') input.sources = rule.types;
  if (rule.id) input.id = rule.id;
  if (rule.description.trim()) input.description = rule.description.trim();
  if (hasWorkloadFilters(rule.types)) {
    if (rule.namespace) input.namespace = rule.namespace;
    if (rule.workload) input.workload = rule.workload;
  }
  if (single === 'troubleshoot') {
    if (rule.alert) input.aggregation_key = rule.alert;
    if (rule.severity) input.severity = rule.severity;
    input.delivery_mode = rule.action === 'send' && rule.batch ? 'batch' : 'real_time';
    if (input.delivery_mode === 'batch') input.frequency = 'hourly';
  }
  if (single && RULE_TYPE[single].events) {
    const all = RULE_TYPE[single].events!.length;
    const events = rule.events || RULE_TYPE[single].defaultEvents || [];
    // Automation rules always name their events; an optimization rule for all five names none.
    if (single === 'automation' || events.length < all) input.event_types = events;
  }
  if (rule.action === 'send') input.mappings = rule.mappings;
  return input;
};

// ----------------------------- words -----------------------------

export const listText = (items: string[]) => (items.length < 2 ? items[0] || '' : `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`);
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export const mappingLabel = (m: RuleMapping): string => {
  const c = m.channels || {};
  switch (m.platform) {
    case 'slack':
      return c.name ? `#${c.name}` : 'Slack';
    case 'ms_teams':
      return [c.team_name, c.channels?.[0]?.name || c.name].filter(Boolean).join(' › ') || 'Teams';
    case 'email': {
      if (Array.isArray(c)) return c[0] || 'Email';
      const extra = c.emails?.[0];
      const excluded = c.exclusion_emails?.length || 0;
      const users = excluded ? `your users except ${excluded}` : 'your users';
      return extra ? `${users} and ${extra}` : users;
    }
    default:
      return c.name || m.platform;
  }
};

export const whatText = (rule: Pick<Rule, 'types' | 'events' | 'severity'>): string => {
  if (rule.types === 'all') return 'All notifications';
  const ek = eventTypeOf(rule.types);
  const defs = ek ? RULE_TYPE[ek].events! : [];
  if (ek && rule.events && rule.events.length < defs.length) {
    const names = defs.filter((e) => rule.events!.includes(e.id)).map((e) => e.label.toLowerCase());
    return `${cap(RULE_TYPE[ek].noun)} (${listText(names)})`;
  }
  let text = listText(rule.types.map((t) => RULE_TYPE[t].noun));
  if (rule.severity && onlyTroubleshooting(rule.types)) text = `${cap(rule.severity)} ${text}`;
  return cap(text);
};

export const whoText = (
  rule: Pick<Rule, 'types' | 'scope' | 'accounts' | 'except' | 'namespace' | 'workload' | 'alert'>,
  accountName: (id: string) => string
) => {
  const noAccounts = coversNoAccounts(rule.types);
  let text = noAccounts ? 'every account' : rule.scope === 'some' ? listText(rule.accounts.map(accountName)) || 'no accounts' : 'all accounts';
  if (!noAccounts && rule.scope === 'all' && rule.except.length) text += ` except ${listText(rule.except.map(accountName))}`;
  const filters: string[] = [];
  if (rule.namespace) filters.push(`namespace ${rule.namespace}`);
  if (rule.workload) filters.push(`application ${rule.workload}`);
  if (rule.alert) filters.push(`alert ${rule.alert}`);
  return filters.length ? `${text}, ${filters.join(', ')}` : text;
};

// Rule names start with a letter and hold letters, digits, spaces, _ and -.
export const cleanRuleName = (name: string) =>
  name
    .replace(/[^\w\s-]/g, ' ')
    .replace(/\s+/g, ' ')
    .replace(/^[^A-Za-z]+/, '')
    .trim()
    .slice(0, 120);

export const isValidRuleName = (name: string) => /^[A-Za-z][\w\s-]*$/.test(name);

export const suggestedName = (rule: Rule, accountName: (id: string) => string) => {
  const what = whatText(rule);
  const who =
    coversNoAccounts(rule.types) || rule.scope === 'all'
      ? rule.except.length && !coversNoAccounts(rule.types)
        ? ` except ${listText(rule.except.map(accountName))}`
        : ''
      : rule.accounts.length
      ? ` from ${listText(rule.accounts.map(accountName))}`
      : '';
  // Lower the leading capital only, so acronyms like SLO keep theirs.
  const lower = /^[A-Z][a-z]/.test(what) ? what.charAt(0).toLowerCase() + what.slice(1) : what;
  if (rule.action === 'drop') return cleanRuleName(`${rule.until ? 'Pause' : 'Silence'} ${lower}${who}`);
  const first = rule.mappings[0];
  return cleanRuleName(`${what}${who}${first ? ` to ${mappingLabel(first).replace(/^#/, '')}` : ''}`);
};

export interface RuleStatus {
  tone: 'neutral' | 'info' | 'success' | 'warning' | 'critical';
  text: string;
  title?: string;
}

export const formatWhen = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });

export const timeLeft = (iso: string, now = Date.now()) => {
  const ms = new Date(iso).getTime() - now;
  if (ms <= 0) return 'ended';
  const hours = Math.round(ms / 3600e3);
  if (hours < 1) return `${Math.max(1, Math.round(ms / 60000))}m left`;
  if (hours < 48) return `${hours}h left`;
  return `${Math.round(hours / 24)}d left`;
};

export const ruleStatus = (rule: Rule, now = Date.now()): RuleStatus => {
  if (!rule.active) return { tone: 'neutral', text: 'Off', title: 'This rule is turned off and does nothing' };
  if (isPaused(rule, now)) return { tone: 'warning', text: `Paused · ${timeLeft(rule.until!, now)}`, title: `Until ${formatWhen(rule.until!)}` };
  if (rule.action === 'drop' && rule.until) return { tone: 'neutral', text: 'Pause ended', title: `Ended ${formatWhen(rule.until)}` };
  if (rule.action === 'drop') return { tone: 'critical', text: 'Don’t send' };
  return { tone: 'success', text: rule.batch ? 'Send hourly' : 'Send' };
};

// ----------------------------- what a rule covers -----------------------------

export interface Sample {
  account: string | null;
  namespace?: string;
  workload?: string;
  alert?: string;
  severity?: string;
  event?: string | null;
}

export const covers = (rule: Rule, type: RuleTypeKey, sample: Sample): boolean => {
  if (!typesOf(rule).includes(type)) return false;
  if (RULE_TYPE[type].events && rule.types !== 'all' && rule.events && sample.event && !rule.events.includes(sample.event)) return false;
  if (RULE_TYPE[type].accounts) {
    if (rule.scope === 'some' && !rule.accounts.includes(sample.account || '')) return false;
    if (rule.scope === 'all' && sample.account && rule.except.includes(sample.account)) return false;
  } else if (rule.scope === 'some' && !coversNoAccounts(rule.types)) {
    return false;
  }
  if (rule.namespace && sample.namespace !== rule.namespace) return false;
  if (rule.workload && sample.workload !== rule.workload) return false;
  if (rule.alert && sample.alert !== rule.alert) return false;
  if (rule.severity && (sample.severity || 'high') !== rule.severity) return false;
  return true;
};

export interface RuleIssue {
  kind: 'ended' | 'critical' | 'warning' | 'info';
  text: string;
  fix?: { label: string; rule: Rule; kind: 'delete' } | { label: string; rule: Rule; kind: 'save' };
}

const stops = (rule: Rule, now: number) => rule.active && rule.action === 'drop' && !isEnded(rule, now);

// What goes wrong with a rule on its own or against the others: an ended pause,
// accounts that can never match, or Don't send rules that stop what it sends.
export const ruleIssues = (rule: Rule, others: Rule[], accountIds: string[], accountName: (id: string) => string, now = Date.now()): RuleIssue[] => {
  if (!rule.active) return [];
  if (rule.action === 'drop' && isEnded(rule, now)) {
    return [
      {
        kind: 'ended',
        text: `This pause ended ${formatWhen(rule.until!)}. It no longer does anything.`,
        fix: { label: 'Remove it', rule, kind: 'delete' },
      },
    ];
  }
  const out: RuleIssue[] = [];
  const types = typesOf(rule);
  const noAccountTypes = types.filter((t) => !RULE_TYPE[t].accounts);
  if (rule.scope === 'some' && noAccountTypes.length && rule.types !== 'all' && noAccountTypes.length < types.length) {
    out.push({ kind: 'info', text: `Doesn’t apply to ${listText(noAccountTypes.map((t) => RULE_TYPE[t].noun))}: they always cover every account.` });
  }
  if (rule.action !== 'send') return out;
  // Every (type, event, account) the rule sends; each event of a type with events is checked on its own.
  const combos: { type: RuleTypeKey; event: string | null; account: string | null }[] = [];
  const ek = eventTypeOf(rule.types);
  types.forEach((type) => {
    const defs = RULE_TYPE[type].events;
    const events: (string | null)[] = defs ? (type === ek && rule.events ? rule.events : defs.map((e) => e.id)) : [null];
    const accounts: (string | null)[] = !RULE_TYPE[type].accounts
      ? rule.scope === 'all'
        ? [null]
        : []
      : rule.scope === 'some'
      ? rule.accounts
      : accountIds.filter((a) => !rule.except.includes(a));
    events.forEach((event) => accounts.forEach((account) => combos.push({ type, event, account })));
  });
  const blocked = new Map<string, Rule>();
  combos.forEach(({ type, event, account }) => {
    const sample: Sample = {
      account,
      namespace: rule.namespace || undefined,
      workload: rule.workload || undefined,
      alert: rule.alert || undefined,
      severity: rule.severity || 'high',
      event,
    };
    const by = others.find((o) => o.id !== rule.id && stops(o, now) && covers(o, type, sample));
    if (by) blocked.set(`${type}|${event ?? ''}|${account ?? ''}`, by);
  });
  if (!blocked.size) return out;
  const first = blocked.values().next().value as Rule;
  if (blocked.size === combos.length) {
    const accounts = Array.from(new Set(combos.map((c) => c.account).filter((a): a is string => !!a)));
    let fix: RuleIssue['fix'];
    if (first.scope === 'all' && accounts.length && rule.scope === 'some' && !coversNoAccounts(first.types)) {
      fix = {
        label: `Let ${listText(accounts.map(accountName))} through in “${first.name}”`,
        rule: { ...first, except: Array.from(new Set([...first.except, ...accounts])) },
        kind: 'save',
      };
    } else if (first.scope === 'some' && rule.scope === 'some') {
      const rest = first.accounts.filter((a) => !accounts.includes(a));
      if (rest.length) {
        fix = { label: `Remove ${listText(accounts.map(accountName))} from “${first.name}”`, rule: { ...first, accounts: rest }, kind: 'save' };
      }
    }
    out.push({
      kind: 'warning',
      text: `Never sends: “${first.name}” stops everything this rule covers${first.until ? ` until ${formatWhen(first.until)}` : ''}.`,
      fix,
    });
    return out;
  }
  const names = Array.from(new Set(Array.from(blocked.values()).map((r) => `“${r.name}”`)));
  const parts = Array.from(
    new Set(
      Array.from(blocked.keys()).map((key) => {
        const [type, event, account] = key.split('|');
        // A saved rule can name an event this page no longer lists; show its id.
        const label = event
          ? RULE_TYPE[type as RuleTypeKey].events?.find((e) => e.id === event)?.label.toLowerCase() ?? event
          : RULE_TYPE[type as RuleTypeKey].noun;
        return `${account ? `${accountName(account)} ` : ''}${label}`;
      })
    )
  );
  out.push({ kind: 'info', text: `Partly stopped: ${listText(names)} stops ${listText(parts.slice(0, 3))}${parts.length > 3 ? ' and more' : ''}.` });
  return out;
};
