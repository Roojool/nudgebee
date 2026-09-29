import { queryGraphQL } from '@lib/HttpService';

// One batched document for the admin Overview cards' counts. The gateway parses
// every root field, dispatches each to its own action in parallel, and merges
// the results by alias into a single response — so this is ONE HTTP call for
// nine counts instead of nine calls (same mechanism the /overview page and
// GET_USERS_BY_TENANT already rely on). A field that fails (e.g. customroles on
// a tenant without the feature) comes back as `null` in `data` with an entry in
// `errors`; the rest still return, so the counts stay fail-soft per-alias.
//
// Inline literal `where` objects (not the `__WHERE__` substitution the older
// queries use) are valid GraphQL and resolved from the AST by the gateway —
// verified against the live gateway before committing to this shape.
//
// Counts are scoped to active/enabled rows where the where-schema allows it
// (users/accounts status "active"; llm/mcp/connected status "enabled"). rules,
// groups and channels stay total: groups/channels have no active state, and
// notifications_aggregate_rules can't filter is_active (selectable on the row,
// absent from its where input).
//
// Not here: `channels` (needs the messaging-platform + integration merge that
// apiNotifications.getInstalledTools does — a raw messagingplatforms_list count
// misses integration-based channels) and `roles` count when the custom-roles
// feature is off (returns null, dropped by the card).
export const ADMIN_OVERVIEW_COUNTS = `
query AdminOverviewCounts {
  users: users_aggregate_by_tenant(where: { status: { _eq: "active" } }) { rows { count } }
  groups: usergroups_aggregate(where: {}) { rows { count } }
  rules: notifications_aggregate_rules(where: { source: { _neq: "ai_cost" } }) { rows { count } }
  accounts: accounts_aggregate(where: { status: { _eq: "active" } }) { rows { count } }
  providers: integrations_aggregate(where: { type: { _eq: "llm" }, status: { _eq: "enabled" } }) { rows { count } }
  mcps: integrations_aggregate(where: { type: { _eq: "mcp" }, status: { _eq: "enabled" } }) { rows { count } }
  connected: integrations_aggregate(where: { status: { _eq: "enabled" } }) { rows { count } }
  flags: featureflags_list(where: { account_id: { _is_null: true } }) { rows { status } }
  roles: customroles_list { roles { id } }
}
`;

export interface AdminOverviewCounts {
  users: number | null;
  groups: number | null;
  rules: number | null;
  accounts: number | null;
  providers: number | null;
  mcps: number | null;
  connected: number | null;
  flags: number | null;
  roles: number | null;
}

// null for any count means its field failed or is unavailable (the card renders
// a dash, or drops the stat for `omitOnNull` ones like roles). 0 is preserved.
export async function getAdminOverviewCounts(): Promise<AdminOverviewCounts> {
  const res = await queryGraphQL(ADMIN_OVERVIEW_COUNTS, 'AdminOverviewCounts', {});
  const d: any = res?.data?.data || {};
  const cnt = (x: any) => x?.rows?.[0]?.count ?? null;
  return {
    users: cnt(d.users),
    groups: cnt(d.groups),
    rules: cnt(d.rules),
    accounts: cnt(d.accounts),
    providers: cnt(d.providers),
    mcps: cnt(d.mcps),
    connected: cnt(d.connected),
    flags: Array.isArray(d.flags?.rows) ? d.flags.rows.filter((r: any) => r.status === 'enabled').length : null,
    roles: Array.isArray(d.roles?.roles) ? d.roles.roles.length : null,
  };
}
