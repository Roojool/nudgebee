// Not for OSS
import { Locator, Page, expect } from "@playwright/test";
import { LoginPage } from "../../../../pages/LoginPage";
import { AWSLocators } from "../AWSLocators";
import { RdsLocators } from "./rdsLocators";
import { LIFECYCLE_ACTIONS } from "./rdsConstants";

export type RdsSubTab = "summary" | "instances";

// The shared AWS entry flow — Admin > Integrations status check, then sidenav > Cloud,
// then the account picker — runs against a shared dev environment, and its internal
// budgets occasionally blow under the two-worker CI layout (PW_WORKERS, PR #37745).
// Retrying here keeps the fix inside this module: raising those budgets in AWSLocators
// instead would change a file every AWS spec shares, which pulls the whole
// tests/CloudAccount/AWS tree into `RUN_MODE: affected` on every run of this PR.
//
// Typed to AWSLocators rather than RdsLocators because nothing here is RDS-specific:
// this is the twin of openAccountWithRetry in ../EC2/ec2Helper.ts, which is private to
// that module. Promoting one copy to a shared AWS helper is raised under Follow-ups
// rather than done here, so this PR leaves the merged EC2 module untouched.
//
// Only a timeout is retried. A test.skip() raised when the AWS integration is inactive,
// and any assertion failure, must propagate on the first attempt rather than being
// retried into a slower version of the same outcome.
async function openAccountWithRetry(locators: AWSLocators, attempts = 3): Promise<void> {
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

// Lands on Infra > Cloud > the AWS account and opens one RDS sub-tab the way a person
// does: pick the account, hover the RDS anchor tab, click the entry in its dropdown.
//
// Deliberately NOT goto('/cloud-account/details/<id>#rds/instances'): the account id is
// per-environment data this suite has no business hardcoding, and the page resolves its
// tab from filterOptions, which is only final once cloud_provider has loaded — a cold
// deep link can therefore settle on Summary. openAWSCloudAccountFromConfig also carries
// the integration gate, so an environment with no active AWS account skips rather than
// failing on a missing tab.
export async function openRdsSubTab(page: Page, subTab: RdsSubTab): Promise<RdsLocators> {
  const locators = new RdsLocators(page);

  await new LoginPage(page).doFullLogin();
  await openAccountWithRetry(locators);

  if (subTab === "instances") {
    await locators.navigateToSubTab(locators.AnchorTabRDS, locators.RDSInstances, locators.RDSInstancesUrl);
  } else {
    await locators.navigateToSubTab(locators.AnchorTabRDS, locators.RDSSummary, locators.RDSSummaryUrl);
  }

  // Park the cursor in open content. Left on the anchor strip, the tab's own dropdown
  // reopens over the toolbar below and the next click hits that instead; parked at 0,0
  // it sits on the sidebar rail, whose hover flyout is a Popover with an invisible
  // backdrop that swallows clicks page-wide. Mid-viewport is neither.
  await page.mouse.move(640, 500);

  return locators;
}

// Opens the Instances sub-tab and waits for its first fetch to land, returning how many
// database instances the shared dev account currently holds. Several tests branch on
// this: an account with no RDS rows renders the empty panel instead of a table, and that
// is a legitimate state of the environment rather than a failure of the code under test.
export async function openInstances(page: Page): Promise<{ locators: RdsLocators; rowCount: number }> {
  const locators = await openRdsSubTab(page, "instances");
  await expect(locators.instancesRoot).toBeVisible({ timeout: 60000 });
  const rowCount = await locators.instanceRowCount();
  return { locators, rowCount };
}

// ds/OverlayItem renders a MUI MenuItem with component='div' (internal/Overlay.tsx:283-290),
// and MUI marks a non-button ButtonBase disabled with both aria-disabled and the
// Mui-disabled class. Either signal alone is enough to call the item disabled, so both
// the picker below and the spec's all-disabled assertion read it through here — two
// callers disagreeing on what "disabled" means is how the all-disabled branch ends up
// asserting something its own guard did not require.
export async function isMenuItemDisabled(item: Locator): Promise<boolean> {
  const ariaDisabled = await item.getAttribute("aria-disabled");
  const muiDisabled = await item.evaluate((node) => node.classList.contains("Mui-disabled"));
  return ariaDisabled === "true" || muiDisabled;
}

// The first lifecycle action a row actually offers, or null when every one is greyed
// out. buildMenuItems disables an action the instance's state does not allow and every
// action when the account is read-only (resourceActions.ts:269-274) — an instance in
// 'backing-up', for one, allows none of the three — so an all-disabled menu is a normal
// outcome that the caller asserts on rather than an error.
export async function firstEnabledLifecycleAction(locators: RdsLocators): Promise<string | null> {
  for (const action of LIFECYCLE_ACTIONS) {
    const item = locators.actionMenuItem(action);
    if ((await item.count()) === 0) continue;
    if (!(await isMenuItemDisabled(item))) {
      return action;
    }
  }
  return null;
}
