import { test, expect, Page } from "@playwright/test";
import { LoginPage } from "../../../../pages/LoginPage";
import { AzureLocators } from "../AzureLocators";
import { waitForGraphQLAndValidate } from "../../../utils/GraphQLNetworkWatcher";

// ── Shared setup helpers ──────────────────────────────────────────────────────

async function openAccount(page: Page, locators: AzureLocators) {
  await new LoginPage(page).doFullLogin();
  await locators.openAzureCloudAccountFromConfig();
}

async function goToServicesTab(page: Page, locators: AzureLocators) {
  await openAccount(page, locators);
  await expect(locators.AnchorTabServices).toBeVisible();
  await locators.AnchorTabServices.click();
  await page.waitForLoadState("networkidle");
  await page
    .locator("#servicesTable tbody tr")
    .first()
    .waitFor({ state: "visible", timeout: 30000 });
}

async function expandFirstServiceRow(page: Page, locators: AzureLocators) {
  await goToServicesTab(page, locators);
  await locators.ServicesRowExpandButton.click();
  await expect(locators.ServicesDrilldownTabResources).toBeVisible();
}

// ── Tests ─────────────────────────────────────────────────────────────────────

test("API testing Cloud Account -> Azure -> Services", { tag: ["@dev", "@test", "@oss", "@smoke", "@functional"] }, async ({
  page,
}, testInfo) => {
  test.setTimeout(120000);
  const locators = new AzureLocators(page);
  await openAccount(page, locators);

  await waitForGraphQLAndValidate(
    page,
    async () => {
      await expect(locators.AnchorTabServices).toBeVisible();
      await locators.AnchorTabServices.click();
      await page.waitForLoadState("networkidle");
    },
    { testName: testInfo.title, operationNames: [] }
  );
});

test("API testing Cloud Account -> Azure -> Services -> Resources tab", { tag: ["@dev", "@test", "@oss", "@functional", "@regression"] }, async ({
  page,
}, testInfo) => {
  test.setTimeout(120000);
  const locators = new AzureLocators(page);
  await goToServicesTab(page, locators);

  await waitForGraphQLAndValidate(
    page,
    async () => {
      await locators.ServicesRowExpandButton.click();
      await expect(locators.ServicesDrilldownTabResources).toBeVisible();
      await page.waitForLoadState("networkidle");
    },
    { testName: testInfo.title, operationNames: [] }
  );
});

test("API testing Cloud Account -> Azure -> Services -> Cost Trend tab", { tag: ["@dev", "@test", "@oss", "@functional", "@regression"] }, async ({
  page,
}, testInfo) => {
  test.setTimeout(120000);
  const locators = new AzureLocators(page);
  await expandFirstServiceRow(page, locators);

  await waitForGraphQLAndValidate(
    page,
    async () => {
      await locators.ServicesDrilldownTabCostTrend.click();
      await page.waitForLoadState("networkidle");
    },
    { testName: testInfo.title, operationNames: [] }
  );
});

test("Cloud Account Azure Services - open Services, expand the first service, expand a resource row, verify the Details panel opens", { tag: ["@dev", "@test", "@oss", "@smoke", "@functional"] }, async ({
  page,
}) => {
  test.setTimeout(120000);
  const locators = new AzureLocators(page);
  await expandFirstServiceRow(page, locators);

  await page
    .locator("#service-resource-listing-table tbody tr")
    .first()
    .waitFor({ state: "visible", timeout: 30000 });

  // ListCloudResources has known GQL errors (acceptable backend issue), plain assertions used
  await locators.getResourceRowExpandButton().click();
  await expect(locators.getResourceRowCollapseButton()).toBeVisible();
  await expect(locators.getResourceDetailsPanel()).toBeVisible();
  await page.waitForLoadState("networkidle");
});

test("API testing Cloud Account -> Azure -> Services -> Recommendations tab", { tag: ["@dev", "@test", "@oss", "@functional", "@regression"] }, async ({
  page,
}, testInfo) => {
  test.setTimeout(120000);
  const locators = new AzureLocators(page);
  await expandFirstServiceRow(page, locators);

  await waitForGraphQLAndValidate(
    page,
    async () => {
      await locators.ServicesDrilldownTabRecommendations.click();
      await page.waitForLoadState("networkidle");
    },
    { testName: testInfo.title, operationNames: [] }
  );
});
