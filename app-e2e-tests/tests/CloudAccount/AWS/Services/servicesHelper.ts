// Not for OSS
import { Page, Locator, expect } from "@playwright/test";
import { LoginPage } from "../../../../pages/LoginPage";
import { ServicesLocators } from "./servicesLocators";
import { COLUMN, DRILLDOWN_TABS, ECS_SERVICE_NAMES, OP_LIST_GROUPINGS } from "./servicesConstants";

// The shared AWS entry flow — Admin > Integrations status check, then sidenav > Cloud,
// then the account picker — runs against a shared dev environment, and its internal
// budgets occasionally blow under the two-worker CI layout (PW_WORKERS). The EC2
// module hit this twice on its own PR, both passing on Playwright's retry; retrying
// here keeps the fix inside this module rather than touching AWSLocators, which every
// AWS spec shares. Only a timeout is retried — a test.skip() raised when the AWS
// integration is inactive, and any assertion failure, propagate on the first attempt.
async function openAccountWithRetry(locators: ServicesLocators, attempts = 3): Promise<void> {
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      await locators.openAWSCloudAccountFromConfig();
      return;
    } catch (error) {
      const isTimeout =
        error instanceof Error && (error.name === "TimeoutError" || /Timeout \d+ms exceeded/.test(error.message));
      if (attempt === attempts || !isTimeout) {
        throw error;
      }
      console.log(`AWS cloud account navigation timed out (attempt ${attempt}/${attempts}), retrying...`);
    }
  }
}

// Waits out the fetch. While `loading` is true CustomTable swaps in a skeleton <tbody>
// that carries no id, so the id'd body being attached means the response has landed.
// An account with no spend in the window renders the empty panel in the body's place,
// which is why both are accepted — neither is a failure of the code under test.
export async function waitForServices(locators: ServicesLocators, timeout = 90000): Promise<void> {
  await locators.servicesBody.or(locators.servicesEmpty).first().waitFor({ state: "attached", timeout });
}

// Lands on Infra > Cloud > the AWS account and opens the Services tab the way a person
// does: pick the account, click the anchor tab. Deliberately NOT
// goto('/cloud-account/details/<id>#services'): the account id is per-environment data
// this suite has no business hardcoding, and openAWSCloudAccountFromConfig carries the
// integration gate, so an environment with no active AWS account skips rather than
// failing on a missing tab.
export async function openServicesTab(page: Page): Promise<ServicesLocators> {
  const locators = new ServicesLocators(page);

  await new LoginPage(page).doFullLogin();
  await openAccountWithRetry(locators);

  await expect(locators.AnchorTabServices).toBeVisible({ timeout: 60000 });
  await locators.AnchorTabServices.click();
  await page.waitForURL(/#services/, { timeout: 30000 });

  // Park the cursor in open content. Left on the anchor strip, a sibling tab's own
  // dropdown opens over the toolbar below and the next click hits that instead; parked
  // at 0,0 it sits on the sidebar rail, whose hover flyout is a Popover with an
  // invisible backdrop that swallows clicks page-wide. Mid-viewport is neither.
  await page.mouse.move(640, 500);

  await expect(locators.listingRoot).toBeVisible({ timeout: 60000 });
  await waitForServices(locators);
  return locators;
}

// Only for a baseline taken before any interaction. After an action that refetches,
// assert the expected end state with a retrying expect(...) instead — a plain count
// races the in-flight request and reads the stale table.
export async function serviceRowCount(locators: ServicesLocators): Promise<number> {
  await waitForServices(locators);
  return locators.servicesRows.count();
}

export interface GroupingsQuery {
  query: string;
  variables: { limit?: number; offset?: number };
}

export interface GroupingsQueryLog {
  entries: GroupingsQuery[];
}

// api1/resources.getResourceGroupings inlines its where / group_by / order_by into
// the query text with gqlStringify rather than sending them as variables, so the
// committed filter and sort state is readable straight off the outgoing request. That
// is what keeps these assertions independent of how many services the shared dev
// account happens to bill in the window the suite runs. Hung off "response" rather
// than "request": a logged entry then means the data has landed, not merely that the
// browser asked for it, so an assertion cannot run in the gap before the re-render.
export function trackGroupingsQueries(page: Page): GroupingsQueryLog {
  const log: GroupingsQueryLog = { entries: [] };
  page.on("response", (res) => {
    const req = res.request();
    if (req.method() !== "POST" || !req.url().includes("api/graphql")) return;
    const postData = req.postData();
    if (!postData) return;
    try {
      const payload = JSON.parse(postData);
      const operations = Array.isArray(payload) ? payload : [payload];
      for (const op of operations as { operationName?: string; query?: string; variables?: GroupingsQuery["variables"] }[]) {
        if (op.operationName === OP_LIST_GROUPINGS && op.query) log.entries.push({ query: op.query, variables: op.variables ?? {} });
      }
    } catch {
      // A body that is not JSON still carries the operation name in its text.
      if (postData.includes(OP_LIST_GROUPINGS)) log.entries.push({ query: postData, variables: {} });
    }
  });
  return log;
}

// The services listing groups by service + account + tenant
// (CloudAccountServices.tsx:262); the filter-option and previous-period lookups group by
// a single column, and the Resources drill-down groups by resource_id first.
export function isServicesListing(entry: GroupingsQuery): boolean {
  return entry.query.includes('group_by:["resource_service_name","account_id","tenant_id"]');
}

export function isResourcesListing(entry: GroupingsQuery): boolean {
  return entry.query.includes('group_by:["resource_id",');
}

// Waits for a listing query that actually satisfies the predicate, rather than for
// "the next query to arrive": the tab issues several groupings requests on mount and
// the filter-option lookups reuse the same operation, so a bare next-entry read can
// return one nobody asked for. Only safe for a positive predicate.
export async function nextQueryMatching(
  log: GroupingsQueryLog,
  countBefore: number,
  predicate: (entry: GroupingsQuery) => boolean,
  description: string
): Promise<GroupingsQuery> {
  await expect
    .poll(() => log.entries.slice(countBefore).some(predicate), { timeout: 45000, message: `no ${OP_LIST_GROUPINGS} query matched ${description}` })
    .toBe(true);
  return log.entries.slice(countBefore).find(predicate) as GroupingsQuery;
}

// gqlStringify renders `{resource_service_name:{_eq:"AmazonEC2"}}` with no spaces.
export function serviceNameFilter(query: string): string | null {
  return query.match(/resource_service_name:\{_eq:"([^"]+)"\}/)?.[1] ?? null;
}

export function regionFilter(query: string): string | null {
  return query.match(/resource_region:\{_eq:"([^"]+)"\}/)?.[1] ?? null;
}

// order_by is rendered with the `order` enum unquoted: `[{column:"sum_spend_amount",order:asc_nulls_last}]`.
export function orderBy(query: string): { column: string; order: string } | null {
  const match = query.match(/order_by:\[\{column:"([^"]+)",order:(\w+)\}\]/);
  return match ? { column: match[1], order: match[2] } : null;
}

export function spendWindow(query: string): { gte: Date; lte: Date } | null {
  const match = query.match(/spend_date:\{_between:\{_gte:"([^"]+)",_lte:"([^"]+)"\}\}/);
  return match ? { gte: new Date(match[1]), lte: new Date(match[2]) } : null;
}

// Currency renders "$1,234.56", "<$1" for anything under a dollar and "-" for a missing
// value (Currency.jsx:7-13). "<$1" is read as zero so it sorts below every real amount.
export function parseCurrency(text: string): number | null {
  if (text.trim().startsWith("<")) return 0;
  const cleaned = text.replace(/[^0-9.-]/g, "");
  if (!cleaned || cleaned === "-") return null;
  const value = Number(cleaned);
  return Number.isFinite(value) ? value : null;
}

// Opens a toolbar filter and commits `label`, coping with both panel shapes: short
// lists render no search box at all.
export async function chooseFilter(locators: ServicesLocators, trigger: Locator, label: string): Promise<void> {
  await trigger.click();
  // Waits on the panel's options rather than on its search box: the box only exists
  // above eight options, so waiting for it would burn a full timeout on a short list
  // just to conclude it was never coming.
  await locators.visibleFilterOptions().first().waitFor({ state: "visible", timeout: 20000 });

  // Options and the search box render in the same pass, so by this point its absence
  // is a settled answer rather than a race, and needs no timeout.
  const search = locators.filterSearchInput();
  if (await search.isVisible()) {
    await search.fill(label);
  }

  await locators.filterOption(label).click();
  // The trigger shows its label plus the committed value, so this is the signal that
  // the selection landed rather than a fixed pause.
  await expect(trigger).toContainText(label, { timeout: 15000 });
}

// Opens a filter and reads its first option, closing the panel again without choosing.
// Returns null when the account offers no value for that filter — a legitimate state of
// the environment, which the caller turns into a named skip rather than a failure.
export async function firstFilterOption(page: Page, locators: ServicesLocators, trigger: Locator): Promise<string | null> {
  await trigger.click();
  const options = locators.visibleFilterOptions();
  // A filter with nothing to offer renders its "No options" copy instead of rows, so
  // the wait has to accept absence as an answer rather than throw.
  const listed = await options
    .first()
    .waitFor({ state: "visible", timeout: 15000 })
    .then(() => true)
    .catch(() => false);
  const label = listed ? ((await options.first().innerText()) ?? "").trim() : null;
  await page.keyboard.press("Escape");
  await expect(options).toHaveCount(0, { timeout: 10000 });
  return label || null;
}

// The first service row whose Resources cell is a positive count and whose name is
// not the ECS special case, or -1 when there is none. The Resources drill-down for a
// service with zero resources is legitimately empty, and CloudAccountResources swaps
// in a different table for ECS, so the drill-down cases need this row specifically.
export async function expandableServiceRow(locators: ServicesLocators): Promise<number> {
  const count = await serviceRowCount(locators);
  for (let i = 0; i < count; i++) {
    const name = ((await locators.columnCells(COLUMN.serviceName).nth(i).innerText()) ?? "").trim();
    const resources = Number(((await locators.columnCells(COLUMN.resources).nth(i).innerText()) ?? "").replace(/[^0-9]/g, ""));
    if (resources > 0 && !ECS_SERVICE_NAMES.includes(name)) return i;
  }
  return -1;
}

export async function serviceNameAt(locators: ServicesLocators, rowIndex: number): Promise<string> {
  return ((await locators.columnCells(COLUMN.serviceName).nth(rowIndex).innerText()) ?? "").trim();
}

// Expands one service row and lands on the named drill-down tab. The tab strip is
// asserted before the click because it only exists once the row's panel has mounted.
export async function openDrilldown(locators: ServicesLocators, rowIndex: number, tabName: string): Promise<void> {
  const toggle = locators.expandToggle(rowIndex);
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true", { timeout: 15000 });
  await expect(locators.drilldownTab(DRILLDOWN_TABS[0])).toBeVisible({ timeout: 30000 });
  if (tabName !== DRILLDOWN_TABS[0]) {
    await locators.drilldownTab(tabName).click();
  }
  await expect(locators.drilldownTab(tabName)).toHaveAttribute("aria-selected", "true", { timeout: 15000 });
  await expect(locators.drilldownPanel(tabName)).toBeVisible({ timeout: 30000 });
}
