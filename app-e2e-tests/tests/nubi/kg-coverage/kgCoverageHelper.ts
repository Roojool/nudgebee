// Not for OSS
import { Page, Locator, expect } from "@playwright/test";
import { LoginPage } from "../../../pages/LoginPage";
import { NubiLocators } from "../nubiLocators";
import { KgCoverageLocators, FLOW_SOURCE_LABELS } from "./kgCoverageLocators";

// app/src/api1/knowledge-graph/index.ts names the three calls the Coverage tab makes this way.
export const OP_CLOUD_ACCOUNTS = "KgGetCloudAccounts";
export const OP_TENANT_FILTER = "KgGetTenantFilter";
export const OP_UPSERT_FILTER = "KgUpsertTenantFilter";

export interface CloudAccountRow {
  id: string;
  account_name?: string | null;
  account_number?: string | null;
  cloud_provider?: string | null;
}

export interface TenantFilter {
  account_ids?: string[] | null;
  flow_sources?: string[] | null;
}

export interface CoverageLoad {
  accounts: CloudAccountRow[];
  filter: TenantFilter | null;
}

export interface CheckboxState {
  label: string;
  checked: boolean;
}

// Resolves with the JSON body of the next /api/graphql response carrying `operationName`.
export async function captureOperation(page: Page, operationName: string, timeout = 60000): Promise<any> {
  return (await captureOperationExchange(page, operationName, timeout)).body;
}

// Same as captureOperation, but also hands back the GraphQL document that was sent, for asserting mutation arguments.
export async function captureOperationExchange(page: Page, operationName: string, timeout = 60000): Promise<{ body: any; query: string }> {
  const response = await page.waitForResponse(
    (res) => {
      if (!res.url().includes("api/graphql") || res.request().method() !== "POST") return false;
      return (res.request().postData() ?? "").includes(`"operationName":"${operationName}"`);
    },
    { timeout }
  );
  expect(response.status(), `${operationName} did not return HTTP 200`).toBe(200);
  const sent = JSON.parse(response.request().postData() ?? "{}");
  return { body: await response.json(), query: String(sent.query ?? "") };
}

// Logs in, opens the Nubi panel and lands on b-Cortex > Knowledge Graph with the Settings control on screen.
export async function openKnowledgeGraph(page: Page): Promise<KgCoverageLocators> {
  const loginPage = new LoginPage(page);
  const nubi = new NubiLocators(page);
  const locators = new KgCoverageLocators(page);

  await loginPage.doFullLogin();
  await nubi.openPanel();
  await nubi.openBCortex(locators.knowledgeGraphTab);
  await locators.knowledgeGraphTab.click();
  await expect(locators.kgCanvas).toBeVisible({ timeout: 60000 });
  await expect(locators.kgSettingsBtn, "the Knowledge Graph Settings control is missing — it renders only for a tenant admin").toBeVisible({
    timeout: 30000,
  });
  return locators;
}

// Opens Settings, which lands on Coverage, and returns the account list and saved filter the tab loaded on mount.
export async function openCoverage(page: Page, locators: KgCoverageLocators): Promise<CoverageLoad> {
  // Registered before the click, since KGCoverageTab fires both reads the moment it mounts.
  const accountsRes = captureOperation(page, OP_CLOUD_ACCOUNTS);
  const filterRes = captureOperation(page, OP_TENANT_FILTER);
  await locators.kgSettingsBtn.click();
  await expect(locators.settingsDialog).toBeVisible({ timeout: 30000 });
  const [accountsBody, filterBody] = await Promise.all([accountsRes, filterRes]);
  expect(accountsBody.errors, `${OP_CLOUD_ACCOUNTS} returned GraphQL errors`).toBeFalsy();
  expect(filterBody.errors, `${OP_TENANT_FILTER} returned GraphQL errors`).toBeFalsy();
  await waitForCoverageLoaded(locators);
  return {
    accounts: accountsBody?.data?.cloud_accounts?.rows ?? [],
    filter: filterBody?.data?.kg_get_tenant_filter ?? null,
  };
}

// Opens Coverage and fails by name when the tenant has no active cloud account, which every account-level case needs.
export async function openCoverageWithAccounts(page: Page, locators: KgCoverageLocators): Promise<CoverageLoad> {
  const load = await openCoverage(page, locators);
  expect(load.accounts.length, "the tenant has no active cloud account, so the Coverage account list cannot be exercised").toBeGreaterThan(0);
  await expect(locators.accountCheckboxes).toHaveCount(load.accounts.length, { timeout: 30000 });
  return load;
}

// The tab shows "Loading…" until both reads resolve, then flow sources plus either account rows or the no-accounts copy.
export async function waitForCoverageLoaded(locators: KgCoverageLocators): Promise<void> {
  await expect(locators.coverageDescription).toBeVisible({ timeout: 30000 });
  await expect(locators.accountsLoading).toHaveCount(0, { timeout: 40000 });
  await expect(locators.flowSourceCheckboxes).toHaveCount(FLOW_SOURCE_LABELS.length, { timeout: 30000 });
  await expect(locators.accountCheckboxes.first().or(locators.noAccountsState)).toBeVisible({ timeout: 30000 });
}

// Reads every checkbox in a column as label + checked, in render order.
export async function readCheckboxStates(checkboxes: Locator): Promise<CheckboxState[]> {
  return checkboxes.evaluateAll((inputs) =>
    inputs.map((input) => ({
      label: (input.closest("label")?.textContent ?? "").trim(),
      checked: (input as HTMLInputElement).checked,
    }))
  );
}

// The account row label KGCoverageTab renders: name, else number, else id.
export function accountLabel(account: CloudAccountRow): string {
  return account.account_name || account.account_number || account.id;
}

// Mirrors KGCoverageTab's filteredAccounts: a case-insensitive substring over name, number, provider and id.
export function expectedMatchCount(accounts: CloudAccountRow[], term: string): number {
  const q = term.trim().toLowerCase();
  return accounts.filter((acc) =>
    [acc.account_name, acc.account_number, acc.cloud_provider, acc.id].some((field) => (field || "").toString().toLowerCase().includes(q))
  ).length;
}

// Mirrors the tab's seeding rule: an empty saved list means "all", otherwise exactly the saved ids.
export function expectedAccountChecked(load: CoverageLoad, account: CloudAccountRow): boolean {
  const saved = load.filter?.account_ids ?? [];
  return saved.length === 0 || saved.includes(account.id);
}

// KGCoverageTab's FLOW_SOURCES ids; the backend expands an empty flow_sources list to exactly these.
export const TOGGLEABLE_FLOW_SOURCE_IDS = ["ebpf", "traces", "datadog-apm", "newrelic-apm"];

// Why an unchanged Save would still remove something, or null when it is a true no-op.
// A saved id the tab cannot show (an inactive account, a non-toggleable flow source) is dropped when a full selection collapses to [].
export function unsafeSaveReason(load: CoverageLoad): string | null {
  const activeIds = new Set(load.accounts.map((acc) => acc.id));
  const staleAccounts = (load.filter?.account_ids ?? []).filter((id) => !activeIds.has(id));
  if (staleAccounts.length > 0) return `the saved filter holds ${staleAccounts.length} account id(s) that are not active cloud accounts`;
  const hiddenSources = (load.filter?.flow_sources ?? []).filter((id) => !TOGGLEABLE_FLOW_SOURCE_IDS.includes(id));
  if (hiddenSources.length > 0) return `the saved filter holds flow source(s) the tab does not show: ${hiddenSources.join(", ")}`;
  return null;
}

export function expectedFlowSourcesChecked(load: CoverageLoad): Record<string, boolean> {
  const ids: Record<string, string> = { eBPF: "ebpf", Traces: "traces", "Datadog APM": "datadog-apm", "New Relic APM": "newrelic-apm" };
  const saved = load.filter?.flow_sources ?? [];
  const result: Record<string, boolean> = {};
  for (const label of FLOW_SOURCE_LABELS) result[label] = saved.length === 0 || saved.includes(ids[label]);
  return result;
}

// Asserts every checkbox on screen matches the saved filter the tab loaded, which is what a fresh open must show.
export async function expectCheckboxesMirrorSavedFilter(locators: KgCoverageLocators, load: CoverageLoad): Promise<void> {
  const flow = expectedFlowSourcesChecked(load);
  for (const label of FLOW_SOURCE_LABELS) {
    const box = locators.flowSourceCheckbox(label);
    if (flow[label]) await expect(box, `flow source ${label} should be checked`).toBeChecked();
    else await expect(box, `flow source ${label} should be unchecked`).not.toBeChecked();
  }
  const onScreen = (await readCheckboxStates(locators.accountCheckboxes)).map((s) => `${s.label} | ${s.checked}`).sort();
  const expected = load.accounts.map((acc) => `${accountLabel(acc)} | ${expectedAccountChecked(load, acc)}`).sort();
  expect(onScreen, "the account checkboxes do not match the saved Knowledge Graph filter").toEqual(expected);
}

// A label + checked snapshot of both columns, for comparing the tab before and after a close or save.
export async function snapshotCoverage(locators: KgCoverageLocators): Promise<{ accounts: CheckboxState[]; flowSources: CheckboxState[] }> {
  return {
    accounts: await readCheckboxStates(locators.accountCheckboxes),
    flowSources: await readCheckboxStates(locators.flowSourceCheckboxes),
  };
}

// Closes Settings with its X and waits for the dialog to unmount, which drops all unsaved Coverage state.
export async function closeSettings(locators: KgCoverageLocators): Promise<void> {
  await locators.settingsCloseBtn.click();
  await locators.settingsDialog.waitFor({ state: "detached", timeout: 20000 });
}

// Types into the account search and waits for the value to commit.
export async function searchAccounts(locators: KgCoverageLocators, term: string): Promise<void> {
  await locators.accountSearchInput.fill(term);
  await expect(locators.accountSearchInput).toHaveValue(term, { timeout: 10000 });
}
