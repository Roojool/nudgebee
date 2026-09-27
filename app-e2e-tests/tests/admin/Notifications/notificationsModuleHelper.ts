// Not for OSS
import { Page, expect } from "@playwright/test";
import { LoginPage } from "../../../pages/LoginPage";
import { NotificationsModuleLocators } from "./notificationsModuleLocators";
import { NOTIFICATIONS_PATH } from "./notificationsModuleConstants";

// Lands on Admin > Notifications and waits until the rules listing has settled.
//
// Goes to the fragment directly rather than clicking through the Admin flyout: the
// /user-management tab effect reads router.asPath's fragment on mount and only rewrites
// the URL when the fragment is missing or names a disabled tab, so #notifications is
// honoured on a cold load. The sidebar route is covered by the tab-navigation test.
export async function openNotificationsTab(page: Page): Promise<NotificationsModuleLocators> {
  const locators = new NotificationsModuleLocators(page);
  await new LoginPage(page).doFullLogin();

  await page.goto(NOTIFICATIONS_PATH);
  await expect(locators.notificationsTab).toHaveAttribute("data-tab-selected", "true", { timeout: 60000 });

  await waitForRulesListing(locators);
  return locators;
}

// index.tsx starts `loading` true so the table renders a skeleton on mount, then its rows.
// The "When no rule matches" row is always there, even for a tenant with no rules.
export async function waitForRulesListing(locators: NotificationsModuleLocators): Promise<void> {
  await expect(locators.listingRoot).toBeVisible({ timeout: 60000 });
  await expect(locators.fallbackRow).toBeVisible({ timeout: 60000 });
}

export async function openCreateRuleModal(locators: NotificationsModuleLocators): Promise<void> {
  await expect(locators.notificationRuleBtn).toBeVisible({ timeout: 30000 });
  await locators.notificationRuleBtn.click();
  await locators.blankRuleItem.click();
  await expect(locators.ruleDialog).toBeVisible({ timeout: 30000 });
  await expect(locators.notificationNameInput).toBeVisible({ timeout: 30000 });
}

// A one-hour Don't send for the weekly digest: the digest goes out once a week, so a
// one-hour pause almost never meets it, and it ends by itself if cleanup never runs.
// That keeps the suite safe on a shared tenant, and it needs no one's real channel.
export async function makeRuleInert(page: Page, locators: NotificationsModuleLocators): Promise<void> {
  await locators.selectOnlyType(locators.weeklyDigestTab);
  await locators.dontSendAction.click();
  await locators.pauseForSelector.click();
  await page.getByRole("option", { name: "1 hour" }).click();
}

// Creates one inert rule and proves it persisted by reading it back out of the listing.
// The success toast alone is not the assertion: the row is, because the page reloads
// its rules from the server after the editor closes.
export async function createSuppressedRule(page: Page, locators: NotificationsModuleLocators, ruleName: string): Promise<void> {
  await openCreateRuleModal(locators);
  await makeRuleInert(page, locators);
  await locators.notificationNameInput.fill(ruleName);
  await expect(locators.notificationNameInput).toHaveValue(ruleName);

  await locators.ruleSubmitBtn.click();

  // The editor closes only on success, so a form left open means a check refused the input.
  await expect(locators.ruleDialog).toBeHidden({ timeout: 60000 });
  await expect(locators.rowByName(ruleName)).toHaveCount(1, { timeout: 60000 });
}

// All GraphQL calls share one endpoint, so match the ListNotificationRules operation name; attach before the triggering action.
export function waitForRulesQuery(page: Page, timeout = 60000) {
  return page.waitForResponse((response) => response.request().postData()?.includes("ListNotificationRules") ?? false, { timeout });
}

// The dialog also closes on a failed delete; only a successful one triggers the list re-fetch, so that response is the signal.
export async function confirmDeleteAndWaitForRelist(page: Page, locators: NotificationsModuleLocators): Promise<void> {
  await Promise.all([waitForRulesQuery(page), locators.deleteConfirmBtn.click()]);
  await expect(locators.deleteDialog).toBeHidden({ timeout: 30000 });
  await waitForRulesListing(locators);
}

// Removes a rule this suite created. Tolerant of the rule already being gone so it is
// safe to call from cleanup after a test that failed before creating anything.
export async function deleteRuleByName(page: Page, locators: NotificationsModuleLocators, ruleName: string): Promise<void> {
  const row = locators.rowByName(ruleName);
  // A rule that was never created is the normal case in cleanup, so absence is a branch
  // rather than a failure.
  const exists = await row
    .first()
    .waitFor({ state: "visible", timeout: 10000 })
    .then(() => true)
    .catch(() => false);
  if (!exists) return;

  await locators.openDeleteForRule(ruleName);
  await expect(locators.deleteDialog).toBeVisible({ timeout: 30000 });
  await confirmDeleteAndWaitForRelist(page, locators);
  await expect(locators.rowByName(ruleName)).toHaveCount(0, { timeout: 60000 });
}
