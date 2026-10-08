// The account an event comes from is not a filter clause: the trigger's `accounts` param
// names the accounts it listens to, and the server routes only those accounts' events to it.
export const STRUCTURED_FILTER_FIELDS = [
  { filterType: 'event_type', eventField: 'event_type', label: 'Event Type' },
  { filterType: 'namespace', eventField: 'subject_namespace', label: 'Namespace' },
  { filterType: 'source', eventField: 'source', label: 'Source' },
  { filterType: 'priority', eventField: 'priority', label: 'Priority' },
] as const;

const UUID = '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}';

// Older filters compare the picker's UUID against `event.cluster`, which carries a name and
// never matches. Only the UUID form is rewritten — a name-valued `event.cluster` was authored
// by hand or by the AI builder and is correct as-is.
const LEGACY_CLUSTER_UUID_RE = new RegExp(`event\\.cluster(\\s*==\\s*)"(${UUID})"`, 'g');

export const canonicalizeFilterExpression = (filter: string): string =>
  (filter || '').replace(LEGACY_CLUSTER_UUID_RE, 'event.cloud_account_id$1"$2"');

// `cluster` and `subject_namespace` are overloaded columns: k8s producers write a cluster
// name and a namespace, cloud-collector writes the cloud account name and the cloud service
// name (collector-server/cloud-collector/account/etl_events.go prepareEventForDB). The filter
// field is identical either way — only what we call it changes.
export const isCloudProvider = (cloudProvider?: string): boolean => !!cloudProvider && cloudProvider !== 'K8s';

const CLOUD_FIELD_LABELS: Record<string, { label: string; description: string }> = {
  namespace: { label: 'Service', description: 'Cloud service (e.g. AmazonEC2, AWS_RDS)' },
};

const K8S_FIELD_DESCRIPTIONS: Record<string, string> = {
  namespace: 'Kubernetes namespace',
};

export const structuredFieldLabel = (filterType: string, cloudProvider?: string): string => {
  if (isCloudProvider(cloudProvider) && CLOUD_FIELD_LABELS[filterType]) {
    return CLOUD_FIELD_LABELS[filterType].label;
  }
  return STRUCTURED_FILTER_FIELDS.find((f) => f.filterType === filterType)?.label ?? '';
};

export const structuredFieldDescription = (filterType: string, cloudProvider?: string): string => {
  if (isCloudProvider(cloudProvider) && CLOUD_FIELD_LABELS[filterType]) {
    return CLOUD_FIELD_LABELS[filterType].description;
  }
  return K8S_FIELD_DESCRIPTIONS[filterType] ?? '';
};

/** Structured filter values: each field holds the values an event may have (any of them matches). */
export type FilterValues = Record<string, string[]>;

// Backslash-escaped, so a value holding a quote still matches the event verbatim.
const quoteValue = (v: string): string => `"${v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
const unquoteValue = (v: string): string => v.replace(/\\(.)/g, '$1');

// One value compiles to `==` (the form older filters use); several to the same `in [...]`
// list the server builds for optimization triggers.
const fieldCondition = (eventField: string, values: string[]): string => {
  const quoted = values.map(quoteValue);
  return quoted.length === 1 ? `event.${eventField} == ${quoted[0]}` : `event.${eventField} in [${quoted.join(', ')}]`;
};

export const buildFilterExpression = (currentValues: FilterValues, overrides?: FilterValues): string => {
  const conditions: string[] = [];
  for (const field of STRUCTURED_FILTER_FIELDS) {
    const values = (overrides?.[field.filterType] ?? currentValues[field.filterType] ?? []).filter(Boolean);
    if (values.length > 0) {
      conditions.push(fieldCondition(field.eventField, values));
    }
  }
  if (conditions.length === 0) {
    return '';
  }
  return `{{ ${conditions.join(' and ')} }}`;
};

// Migrate legacy params.event_type (scalar or array) into a Jinja filter clause
// so old workflows can be edited via the structured-filter UI.
const legacyEventTypeToFilter = (rawEventType: unknown): string => {
  const items = (Array.isArray(rawEventType) ? rawEventType : [rawEventType]).filter((v): v is string => typeof v === 'string' && v !== '');
  // Reuse the structured builder so an array becomes one `in [...]` clause, which also keeps
  // its meaning when it is AND-ed with an existing filter (a bare `or` chain would not).
  return items.length > 0 ? buildFilterExpression({ event_type: items }).replace(/^\{\{\s*|\s*\}\}$/g, '') : '';
};

export const mergeLegacyEventType = (rawEventType: unknown, existingFilter: string): string => {
  const legacy = legacyEventTypeToFilter(rawEventType);
  if (!legacy) {
    return existingFilter || '';
  }
  if (!existingFilter) {
    return `{{ ${legacy} }}`;
  }
  // Strip outer {{ }} from existing, AND with legacy clause, re-wrap.
  const inner = existingFilter
    .trim()
    .replace(/^\{\{\s*/, '')
    .replace(/\s*\}\}$/, '');
  // A top-level `or` binds looser than the added `and`, so the old filter keeps its own brackets.
  return `{{ ${legacy} and ${/\bor\b/.test(inner) ? `(${inner})` : inner} }}`;
};

// True when the structured pickers can represent `filter` exactly. Quote style and spacing don't
// change what an expression matches, and the server writes single-quoted lists.
export const structuredCoversFilter = (filter: string, parsed: FilterValues): boolean => {
  const normalize = (s: string) => canonicalizeFilterExpression(s).trim().replace(/'/g, '"').replace(/\s+/g, ' ');
  return normalize(buildFilterExpression(parsed)) === normalize(filter);
};

const QUOTED_ITEM_RE = /"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'/g;

export const parseFilterExpression = (filter: string): FilterValues => {
  const result: FilterValues = {};
  if (!filter?.includes('{{')) {
    return result;
  }
  const canonical = canonicalizeFilterExpression(filter);

  for (const field of STRUCTURED_FILTER_FIELDS) {
    const equals = new RegExp(`event\\.${field.eventField}\\s*==\\s*(?:"((?:[^"\\\\]|\\\\.)*)"|'((?:[^'\\\\]|\\\\.)*)')`, 'i').exec(canonical);
    const list = new RegExp(`event\\.${field.eventField}\\s+in\\s*\\[([^\\]]*)\\]`, 'i').exec(canonical);
    if (equals) {
      result[field.filterType] = [unquoteValue(equals[1] ?? equals[2])];
    } else if (list) {
      const items = Array.from(list[1].matchAll(QUOTED_ITEM_RE), (m) => unquoteValue(m[1] ?? m[2]));
      if (items.length > 0) result[field.filterType] = items;
    }
  }
  return result;
};

const ACCOUNT_CLAUSE_RE = new RegExp(`^event\\.cloud_account_id\\s*==\\s*"(${UUID})"$`);

// Splits `expr` on a top-level boolean keyword, ignoring keywords inside quotes or parentheses.
const splitTopLevel = (expr: string, keyword: 'and' | 'or'): string[] => {
  const parts: string[] = [];
  let depth = 0;
  let quote = '';
  let start = 0;
  for (let i = 0; i < expr.length; i++) {
    const ch = expr[i];
    if (quote) {
      if (ch === quote && expr[i - 1] !== '\\') quote = '';
    } else if (ch === '"' || ch === "'") {
      quote = ch;
    } else if (ch === '(') {
      depth++;
    } else if (ch === ')') {
      depth--;
    } else if (depth === 0 && /\s/.test(ch)) {
      const match = new RegExp(`^\\s+${keyword}\\s+`, 'i').exec(expr.slice(i));
      if (match) {
        parts.push(expr.slice(start, i).trim());
        i += match[0].length - 1;
        start = i + 1;
      }
    }
  }
  parts.push(expr.slice(start).trim());
  return parts;
};

// Strips parentheses that enclose the whole expression — `(a or b)`, not `(a) or (b)`.
const unwrapParens = (expr: string): string => {
  const trimmed = expr.trim();
  if (!trimmed.startsWith('(') || !trimmed.endsWith(')')) return trimmed;
  let depth = 0;
  let quote = '';
  for (let i = 0; i < trimmed.length - 1; i++) {
    const ch = trimmed[i];
    if (quote) {
      if (ch === quote && trimmed[i - 1] !== '\\') quote = '';
    } else if (ch === '"' || ch === "'") {
      quote = ch;
    } else if (ch === '(') {
      depth++;
    } else if (ch === ')' && --depth === 0) {
      return trimmed;
    }
  }
  return unwrapParens(trimmed.slice(1, -1));
};

// The account uuids a conjunct names when it is only `event.cloud_account_id == "<uuid>"`
// clauses joined by `or`; null when it says anything else.
const accountClauseIds = (conjunct: string): string[] | null => {
  const ids: string[] = [];
  for (const disjunct of splitTopLevel(unwrapParens(conjunct), 'or')) {
    const match = ACCOUNT_CLAUSE_RE.exec(unwrapParens(disjunct));
    if (!match) return null;
    ids.push(match[1]);
  }
  return ids;
};

/**
 * Moves the account a legacy filter pinned (`event.cloud_account_id == "<uuid>"`, or the
 * older uuid-valued `event.cluster`) into the trigger's listened `accounts`, and strips that
 * clause from the filter. The trigger already listened to `accounts`, or to `ownAccountId` when
 * none are saved, so only accounts in both are kept: conversion never changes what fires. Anything
 * it can't rewrite that way (an account clause or-ed with another condition, two account clauses,
 * no overlap, an unknown own account) is returned unchanged, so the sidebar keeps it in advanced mode.
 */
export const migrateLegacyAccountFilter = (
  filter: string,
  accounts: string[] = [],
  ownAccountId = ''
): { filter: string; accounts: string[]; migrated: boolean } => {
  const unchanged = { filter, accounts, migrated: false };
  const canonical = canonicalizeFilterExpression(filter).trim();
  const wrapped = /^\{\{([\s\S]*)\}\}$/.exec(canonical);
  if (!wrapped || wrapped[1].includes('{{') || wrapped[1].includes('}}')) return unchanged;

  let picked: string[] | null = null;
  const remaining: string[] = [];
  for (const conjunct of splitTopLevel(wrapped[1].trim(), 'and')) {
    const ids = accountClauseIds(conjunct);
    if (!ids) {
      remaining.push(conjunct);
    } else if (picked) {
      return unchanged;
    } else {
      picked = ids;
    }
  }
  if (!picked) return unchanged;

  // Saved accounts (else the own account) and the filter both narrowed what fired: keep the overlap.
  const listened = accounts.length > 0 ? accounts : ownAccountId ? [ownAccountId] : [];
  const next = Array.from(new Set(listened.filter((a) => picked!.includes(a))));
  if (next.length === 0) return unchanged;
  return { filter: remaining.length > 0 ? `{{ ${remaining.join(' and ')} }}` : '', accounts: next, migrated: true };
};
