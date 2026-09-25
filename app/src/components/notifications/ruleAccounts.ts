import { safeJSONParse } from 'src/utils/common';

// The accounts a rule is scoped to. The rules list returns json columns as
// encoded strings; a rule for every account has none.
export const ruleAccountIds = (rule: { account_ids?: unknown } | null | undefined): string[] => {
  const raw = typeof rule?.account_ids === 'string' ? safeJSONParse(rule.account_ids) : rule?.account_ids;
  return Array.isArray(raw) ? raw.filter((id: unknown): id is string => typeof id === 'string') : [];
};
