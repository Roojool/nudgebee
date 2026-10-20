import { Page, Locator, expect } from "@playwright/test";
import { LoginPage } from "../../../pages/LoginPage";
import { waitForGraphQLAndValidate } from "../../utils/GraphQLNetworkWatcher";
import { parkCursor } from "../optimizeModuleHelper";
import { DETAILS_TABLE, SecurityLocators, SECURITY_SUBTABS, SecuritySubTab } from "./securityLocators";

// playwright.config.ts sets `baseURL: process.env.BASE_URL`, so an unset BASE_URL does not
// fail here — it fails later as a goto against "about:blank". Named up front so the run
// says which key is missing instead of reporting an unexplained navigation error.
const BASE_URL = process.env.BASE_URL || "";
if (!BASE_URL) {
  throw new Error("BASE_URL is not set — add it to .env / .env.dev");
}

// Preconditions named once, so an environment that never rendered the strip fails with the
// reason rather than with an unexplained missing element on every test.
const NO_STRIP_HINT =
  "The Optimize tab strip never rendered. /optimise gates its tabs on the session's module grants " +
  "(insights / recommendations / autooptimize) — check the run user still has read access to them.";

const NO_SUBSTRIP_HINT =
  "The Security sub-tab strip never rendered. AnchorComponent only draws it once the Security tab is " +
  "the active tab — check the Security tab is still reachable for the run user.";

// A term no image reference can match, so the "no results" assertion is about the filter
// working rather than about what the shared dev tenant happens to hold. Suffixed per run
// because this string reaches the backend query.
export function noMatchImage(): string {
  return `zz-no-such-image-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

// MUI Tab reports its selection through aria-selected. Unlike the URL it is always in step
// with what is actually rendered, so it is what "which sub-tab am I on" is asserted against.
export async function expectSelectedSubTab(tab: Locator): Promise<void> {
  await expect(tab).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
}

export async function expectUnselectedSubTab(tab: Locator): Promise<void> {
  await expect(tab).toHaveAttribute("aria-selected", "false", { timeout: 30000 });
}

// Lands on the Security tab with no sub-fragment. Which sub-tab that opens is left
// unasserted on purpose — it is what the sanity test is there to observe.
export async function openSecurityTab(page: Page): Promise<SecurityLocators> {
  const locators = new SecurityLocators(page);
  await new LoginPage(page).doFullLogin();

  await page.goto("/optimise#security");
  await expect(locators.securityTab, NO_STRIP_HINT).toBeVisible({ timeout: 60000 });
  await expect(locators.securityTab).toHaveAttribute("data-tab-selected", "true", { timeout: 30000 });
  await expect(locators.imageScanSubTab, NO_SUBSTRIP_HINT).toBeVisible({ timeout: 60000 });

  return locators;
}

// Lands on a Security sub-tab by hash. app/src/pages/optimise/index.jsx splits the hash on
// "/" and resolves the child against the Security tab's tabOptions, and AnchorComponent
// parses the same pair for the strip — so a deep link opens the sub-tab directly. Clicking
// the strip is exercised on its own by the navigation tests, so the other tests do not all
// depend on that one interaction.
export async function openSecuritySubTab(page: Page, key: SecuritySubTab): Promise<SecurityLocators> {
  const locators = new SecurityLocators(page);
  await new LoginPage(page).doFullLogin();

  await page.goto(`/optimise#security/${SECURITY_SUBTABS[key].fragment}`);
  await expect(locators.securityTab, NO_STRIP_HINT).toBeVisible({ timeout: 60000 });
  await expect(locators.securityTab).toHaveAttribute("data-tab-selected", "true", { timeout: 30000 });
  await expect(locators.imageScanSubTab, NO_SUBSTRIP_HINT).toBeVisible({ timeout: 60000 });
  await expectSelectedSubTab(subTabLocator(locators, key));

  return locators;
}

export function subTabLocator(locators: SecurityLocators, key: SecuritySubTab): Locator {
  switch (key) {
    case "imageScan":
      return locators.imageScanSubTab;
    case "cisScan":
      return locators.cisScanSubTab;
    case "vmVulnerabilities":
      return locators.vmVulnerabilitiesSubTab;
    case "cloudPosture":
      return locators.cloudPostureSubTab;
  }
}

// The copy SecurityView shows in place of a sub-view when the tenant holds no account of the
// type that sub-tab reports on. Which of the three renders is itself the assertion on that
// branch, so an empty environment still proves the module names the right missing resource.
export const NO_ACCOUNTS_COPY = {
  cluster: /No clusters connected\. Findings appear here once a cluster is connected and scanned\./,
  vm: /No self-hosted VM accounts connected\. Findings appear here once a VM account is added and scanned\./,
  cloud: /No cloud accounts connected\. Findings appear here once an AWS, Azure or GCP account is added and scanned\./,
} as const;

// Settles a sub-tab's body onto one of its two legitimate branches and asserts that branch.
// A shared environment can lack every account of a type, which is a real state of the module
// rather than a failure — so the empty branch is verified against the copy for that sub-tab
// and reported back, and callers assert the view's own controls only when there is a view.
export async function expectSecurityBody(
  locators: SecurityLocators,
  viewRoot: Locator,
  emptyCopy: RegExp
): Promise<boolean> {
  await expect(locators.securityView.or(locators.securityEmpty).first()).toBeVisible({ timeout: 60000 });
  await expect(locators.securityLoading).toHaveCount(0);

  // Safe to read without waiting: the expect above has already settled one of the two
  // branches, so this only asks which one landed. isVisible() reports a missing element as
  // false rather than throwing, so it needs no catch.
  const isEmpty = await locators.securityEmpty.isVisible();
  if (isEmpty) {
    await expect(locators.securityEmpty).toHaveText(emptyCopy);
    return false;
  }

  await expect(viewRoot).toBeVisible({ timeout: 60000 });
  return true;
}

// Waits out the fetch behind one of the four findings tables. CustomTable swaps in an
// unid'd skeleton tbody while loading, so `#<id>-body` being attached means the response has
// landed; the empty state is the other terminal state.
export async function waitForFindingsTable(locators: SecurityLocators, tableId: string, scope: Locator): Promise<void> {
  await locators.waitForTable(tableId, locators.emptyStateFor(tableId, scope));
}

// Rows or the empty state — the two ends of a settled table. Which one a shared environment
// produces is not what any of these tests is about, so this is what they assert instead.
export async function expectTableSettled(locators: SecurityLocators, tableId: string, scope: Locator): Promise<void> {
  await waitForFindingsTable(locators, tableId, scope);
  await expect(locators.rowsFor(tableId).first().or(locators.emptyStateFor(tableId, scope)).first()).toBeVisible({ timeout: 60000 });
}

// What identifies one image-scan finding on screen: the same CVE can sit on many clusters, images, apps and packages.
export interface FindingKey {
  cluster: string;
  cve: string;
  image: string;
  app: string;
  pkg: string;
}

export type ImageScanStatus = "Open" | "Dismissed";

// Opens Image Scan on its Details view; false when the tenant has no cluster to report on.
export async function openImageScanDetails(page: Page): Promise<{ locators: SecurityLocators; populated: boolean }> {
  const locators = await openSecuritySubTab(page, "imageScan");
  const populated = await expectSecurityBody(locators, locators.imageScanRoot, NO_ACCOUNTS_COPY.cluster);
  if (!populated) return { locators, populated };

  await parkCursor(page);
  await locators.imageScanDetailsOption.click();
  await expect(locators.imageScanDetailsOption).toHaveAttribute("aria-checked", "true", { timeout: 30000 });
  // Rows or the empty state must be on screen, or a caller's isVisible() probe reads an attached-but-unrendered body as empty.
  await expectTableSettled(locators, DETAILS_TABLE, locators.imageScanRoot);
  return { locators, populated };
}

// The table body stays attached while it refetches, so the list response is what proves the rows are for the new filter.
async function refetchDetails(page: Page, locators: SecurityLocators, testName: string, action: () => Promise<void>): Promise<void> {
  await waitForGraphQLAndValidate(page, action, {
    testName,
    operationNames: ["list_k8_recommendation"],
    checkDataErrors: true,
    timeoutMs: 60000,
  });
  await expectTableSettled(locators, DETAILS_TABLE, locators.imageScanRoot);
}

export async function selectImageScanStatus(
  page: Page,
  locators: SecurityLocators,
  status: ImageScanStatus,
  testName: string
): Promise<void> {
  await locators.imageScanStatusFilter.click();
  const option = locators.openDropdownOptions.filter({ hasText: status }).first();
  await expect(option, `No "${status}" option in the Status filter`).toBeVisible({ timeout: 30000 });
  await refetchDetails(page, locators, testName, () => option.click());
  await expect(locators.imageScanStatusFilter).toContainText(status, { timeout: 30000 });
}

// Read before any image search: searching an image drops the Image column from the table.
export async function readFindingKey(locators: SecurityLocators, row: Locator, searchedImage?: string): Promise<FindingKey> {
  const headers = (await locators.detailsHeaderCells.allInnerTexts()).map((t) => t.trim());
  const cell = async (name: string): Promise<string> => {
    const index = headers.indexOf(name);
    expect(index, `No "${name}" column in the Details table`).toBeGreaterThan(-1);
    // The CVE cell also stacks ticket and PR links under the id, so only its first line is the id.
    return (await row.locator("td").nth(index).innerText()).split("\n")[0].trim();
  };
  return {
    cluster: await cell("Cluster"),
    cve: await cell("CVE"),
    image: searchedImage ?? (await cell("Image")),
    app: await cell("App"),
    pkg: await cell("Package Id"),
  };
}

// Used only after searchFindingImage, which scopes the rows to the image and hides its column.
export function findingRow(locators: SecurityLocators, key: FindingKey): Locator {
  return locators
    .rowsFor(DETAILS_TABLE)
    .filter({ hasText: key.cluster })
    .filter({ hasText: key.cve })
    .filter({ hasText: key.app })
    .filter({ hasText: key.pkg });
}

// The Details list is paged by severity, so the image search is what brings one finding onto the first page.
export async function searchFindingImage(page: Page, locators: SecurityLocators, image: string, testName: string): Promise<void> {
  await refetchDetails(page, locators, testName, () => locators.searchAndApply(locators.imageSearch, image));
  await expect(locators.imageSearch).toHaveValue(image, { timeout: 30000 });
}

async function openRowAction(locators: SecurityLocators, row: Locator, item: Locator, label: string): Promise<void> {
  await locators.rowMenuTrigger(row).click();
  await expect(item, `No "${label}" entry in the row menu — the run user needs write access to this cluster`).toBeVisible({
    timeout: 30000,
  });
  await item.click();
}

export async function openDismissModalFromRow(locators: SecurityLocators, row: Locator): Promise<void> {
  await openRowAction(locators, row, locators.dismissMenuItem, "Dismiss / snooze");
  await expect(locators.dismissDialog).toBeVisible({ timeout: 30000 });
}

// Row click opens the finding side panel; the package cell is clicked because the CVE cell is an outbound link.
export async function openFindingPanel(locators: SecurityLocators, row: Locator, key: FindingKey): Promise<void> {
  await row.getByText(key.pkg, { exact: true }).first().click();
  await expect(locators.findingPanel).toBeVisible({ timeout: 30000 });
}

export async function pickDismissDuration(locators: SecurityLocators, duration: string): Promise<void> {
  await locators.dismissDurationFilter.click();
  const option = locators.openDropdownOptions.filter({ hasText: duration }).first();
  await expect(option, `No "${duration}" option in the Duration dropdown`).toBeVisible({ timeout: 30000 });
  await option.click();
  await expect(locators.dismissDurationFilter).toContainText(duration, { timeout: 30000 });
}

// What the cleanup needs to undo one dismissal: the request the UI sent, replayed with dismissed=false.
export interface ParkedFinding {
  url: string;
  headers: Record<string, string>;
  accountId: string;
  recommendationId: string;
}

export async function submitDismissModal(page: Page, locators: SecurityLocators, testName: string, reason: string): Promise<ParkedFinding> {
  if (reason) await locators.dismissReasonInput.fill(reason);
  const sent = page.waitForRequest(
    (r) => r.url().includes("api/graphql") && (r.postData() || "").includes("UpdateRecommendationDismissal")
  );
  await waitForGraphQLAndValidate(page, () => locators.dismissConfirmButton.click(), {
    testName,
    operationNames: ["UpdateRecommendationDismissal"],
    checkDataErrors: true,
    timeoutMs: 60000,
  });
  await expect(locators.dismissDialog).toBeHidden({ timeout: 30000 });

  const request = await sent;
  const variables = request.postDataJSON()?.variables ?? {};
  // HTTP/2 pseudo-headers and the old body length cannot be replayed on a new request.
  const headers = Object.fromEntries(
    Object.entries(await request.allHeaders()).filter(([name]) => !name.startsWith(":") && name !== "content-length")
  );
  return {
    url: request.url(),
    headers,
    accountId: variables.account_id,
    recommendationId: variables.recommendation_id,
  };
}

const REACTIVATE_MUTATION = `mutation UpdateRecommendationDismissal($account_id: String!, $recommendation_id: String!, $dismissed: Boolean!, $reason: String, $snoozed_until: String) {
  recommendations_update_dismissal(object: {account_id: $account_id, recommendation_id: $recommendation_id, dismissed: $dismissed, reason: $reason, snoozed_until: $snoozed_until }) { status }
}`;

// Image Scan offers no working Reactivate yet (#39216), so parked findings are returned to Open through the same mutation the UI uses.
export async function restoreParkedFindings(page: Page, parked: ParkedFinding[]): Promise<void> {
  // Every parked finding gets its restore attempt before anything fails, so one bad request cannot strand the rest on the tenant.
  const failures: string[] = [];
  while (parked.length > 0) {
    const finding = parked.pop()!;
    const response = await page.request
      .post(finding.url, {
        headers: finding.headers,
        data: {
          operationName: "UpdateRecommendationDismissal",
          query: REACTIVATE_MUTATION,
          variables: {
            account_id: finding.accountId,
            recommendation_id: finding.recommendationId,
            dismissed: false,
            reason: "",
            snoozed_until: "",
          },
        },
      })
      .catch((error: Error) => error);
    if (response instanceof Error) {
      failures.push(`${finding.recommendationId}: ${response.message}`);
      continue;
    }
    console.log(`[nb-35666] cleanup reactivated a parked finding: HTTP ${response.status()}`);
    if (!response.ok()) failures.push(`${finding.recommendationId}: HTTP ${response.status()}`);
  }
  expect(failures, "Cleanup left these findings dismissed on the tenant").toEqual([]);
}

export async function reactivateFromRow(page: Page, locators: SecurityLocators, row: Locator, testName: string): Promise<void> {
  await waitForGraphQLAndValidate(page, () => openRowAction(locators, row, locators.reactivateMenuItem, "Reactivate"), {
    testName,
    operationNames: ["UpdateRecommendationDismissal"],
    checkDataErrors: true,
    timeoutMs: 60000,
  });
}

export function dismissReason(): string {
  return `qa_nb35666_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`;
}

// Same wording and format DismissModal renders under the Duration dropdown.
export function snoozeReturnCopy(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  return `Returns to the open list on ${date}.`;
}

// Counts dismissal mutations from here on, so a closed modal can be proven to have sent nothing.
export function countDismissalRequests(page: Page): () => number {
  let sent = 0;
  page.on("request", (request) => {
    if (request.url().includes("api/graphql") && (request.postData() || "").includes("UpdateRecommendationDismissal")) sent++;
  });
  return () => sent;
}

export interface PickedFinding {
  locators: SecurityLocators;
  key: FindingKey;
  row: Locator;
}

// Takes the first Open finding in the Details view and narrows the table to its image; null when there is none to act on.
export async function pickOpenFinding(page: Page, testName: string): Promise<PickedFinding | null> {
  const { locators, populated } = await openImageScanDetails(page);
  if (!populated) return null;

  const firstOpen = locators.rowsFor(DETAILS_TABLE).first();
  if (!(await firstOpen.isVisible())) return null;

  const { image } = await readFindingKey(locators, firstOpen);
  await searchFindingImage(page, locators, image, testName);
  // A shared image can hold more findings than one page, so the target is taken from the searched page itself.
  const target = locators.rowsFor(DETAILS_TABLE).first();
  await expect(target).toBeVisible({ timeout: 60000 });
  const key = await readFindingKey(locators, target, image);
  const row = findingRow(locators, key);
  await expect(row).toHaveCount(1, { timeout: 60000 });
  return { locators, key, row };
}
