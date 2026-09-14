// Not for OSS
import { test, expect } from "@playwright/test";
import { trackOperations } from "../egress-filter/egressFilterHelper";
import {
  ALL_ACCOUNTS_LABEL,
  KPI_ACCOUNT_SPEND,
  KPI_CONVERSATIONS,
  KPI_TENANT_SPEND,
  KPI_TODAY_SPEND,
  KPI_VALUE_PATTERN,
  PERIOD_HEADER_PATTERN,
  SCOPE_CHIP_PATTERN,
  STATUS_PILL_PATTERN,
} from "./myUsageLocators";
import {
  OP_BUDGET_STATUS,
  closeAccountPicker,
  currentYear,
  openMyUsage,
  peekAccountPicker,
  remountMyUsage,
  requiredTenantName,
  searchAccountOption,
  selectedAccountLabel,
  switchToAnotherAccount,
  waitForUsageLoaded,
} from "./myUsageHelper";

// Nubi > b-Cortex > Insights > My Usage (app/src/components/llm/LLMConsumptionTab.jsx,
// mounted with readOnly by BCortexModal.jsx). The mount is a personal read view:
// budgets are edited only at Admin > AI & Tools > Budgets & Limits, so nothing here
// writes. Opened from the Ask-Nubi page the modal carries that page's account, so the
// view is account-scoped throughout; the cases read the KPI strip and the usage
// matrix, switch the modal's account filter between accounts and drive the Insights
// sub-tabs, asserting against the budget-status read each action issues rather
// than against on-screen state alone.

test.describe("Nubi My Usage Tab", () => {
  test(
    "Nubi My Usage sanity - open b-Cortex, select Insights, select My Usage, verify the tab is selected, the header names the current year's usage period, the scope chip names the account the header filter shows, a status pill and the metrics description render and no Add Budget control is offered",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openMyUsage(page);

      await expect(locators.myUsageTab).toHaveAttribute("aria-selected", "true");
      await expect(locators.digestsTab).toHaveAttribute("aria-selected", "false");
      await expect(locators.usageHeader).toHaveText(PERIOD_HEADER_PATTERN);
      await expect(locators.usageHeader).toContainText(currentYear());
      // The chip must describe the same scope the filter shows — the page's own
      // account here, or Tenant on a page without one.
      await expect(locators.scopeChip).toHaveText(SCOPE_CHIP_PATTERN);
      const selected = await selectedAccountLabel(locators);
      if (selected === ALL_ACCOUNTS_LABEL) {
        await expect(locators.scopeChip).toHaveText("Tenant");
      } else {
        await expect(locators.scopeChip).toContainText(selected);
      }
      await expect(locators.statusPill).toHaveText(STATUS_PILL_PATTERN);
      await expect(locators.description).toContainText("Monthly limits reset at the beginning of each month");
      // readOnly forces the admin branch off, so the manage action must not render
      // even for a tenant admin.
      await expect(locators.addBudgetBtn).toHaveCount(0);
    }
  );

  test(
    "Nubi My Usage sanity - verify the KPI strip shows the tenant monthly spend tile named after this tenant, the account monthly spend tile named after the account in scope and the monthly conversations tile scoped to all modules, each reading usage / limit or No limit configured with a progress bar only when a limit is configured",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const tenant = requiredTenantName();
      const locators = await openMyUsage(page);
      const account = await selectedAccountLabel(locators);
      expect(account, "the header filter shows no account — this case expects the page's account in scope").not.toBe(ALL_ACCOUNTS_LABEL);

      await expect(locators.kpiTile(KPI_TENANT_SPEND)).toContainText(tenant);
      await expect(locators.kpiTile(KPI_ACCOUNT_SPEND)).toContainText(account);
      await expect(locators.kpiTile(KPI_CONVERSATIONS)).toContainText("Tenant • all modules");

      // Today's spend renders only when a daily limit is enabled somewhere, so it
      // is checked when present rather than required.
      const labels = [KPI_TENANT_SPEND, KPI_ACCOUNT_SPEND, KPI_CONVERSATIONS];
      if ((await locators.kpiTile(KPI_TODAY_SPEND).count()) > 0) labels.push(KPI_TODAY_SPEND);
      for (const label of labels) {
        await expect(locators.kpiValue(label)).toHaveText(KPI_VALUE_PATTERN);
        // A tile is either configured (a bar, no placeholder) or not (the
        // placeholder, no bar) — never both and never neither.
        const placeholders = await locators.kpiPlaceholder(label).count();
        await expect(locators.kpiProgress(label)).toHaveCount(placeholders === 0 ? 1 : 0);
      }
    }
  );

  test(
    "Nubi My Usage sanity - verify the usage matrix heads its columns Scope, Event Analysis and the assistant Chat, lists a Tenant row named after this tenant and an Account row named after the account in scope, and every row reads usage / limit lines, No limits or a dash per module",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const tenant = requiredTenantName();
      const locators = await openMyUsage(page);
      const account = await selectedAccountLabel(locators);
      expect(account, "the header filter shows no account — this case expects the page's account in scope").not.toBe(ALL_ACCOUNTS_LABEL);

      await expect(locators.matrix).toBeVisible({ timeout: 15000 });
      await expect(locators.matrixEventAnalysisHeader).toBeVisible();
      await expect(locators.matrixChatHeader).toBeVisible();
      await expect(locators.matrixScopeLabels).toHaveCount(2);
      await expect(locators.matrixRow("Tenant")).toContainText(tenant);
      await expect(locators.matrixRow("Account")).toContainText(account);
      await expect(locators.matrixRow("Tenant")).toContainText(/\/|No limits|—/);
      await expect(locators.matrixRow("Account")).toContainText(/\/|No limits|—/);
    }
  );

  test(
    "Nubi My Usage sanity - open the b-Cortex account filter, verify it offers at least one real account, search it for All accounts, verify that entry is offered, close it with Escape without picking, verify b-Cortex stays open with the same account in scope and no budget-status read is sent",
    { tag: ["@dev", "@sanity", "@negative", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openMyUsage(page);
      const before = await selectedAccountLabel(locators);
      // Attached after the first load so only a pick could add a read.
      const counts = trackOperations(page, [OP_BUDGET_STATUS]);

      await peekAccountPicker(locators);
      // One real account is the precondition the switching cases below rely on.
      await expect(locators.realAccountOption, "the account picker offers no real account on this tenant").toBeVisible({ timeout: 15000 });
      await searchAccountOption(locators, ALL_ACCOUNTS_LABEL);
      await expect(locators.accountOption(ALL_ACCOUNTS_LABEL)).toBeVisible();

      await closeAccountPicker(page, locators);
      await expect(locators.accountSelect).toHaveText(before);
      await expect(locators.scopeChip).toHaveText(SCOPE_CHIP_PATTERN);
      expect(counts[OP_BUDGET_STATUS]).toBe(0);
    }
  );

  test(
    "Nubi My Usage - switch the b-Cortex account filter to a different account, verify the budget status is re-read once, the account monthly spend tile, the scope chip and the matrix's Account row all switch to naming the new account",
    { tag: ["@dev", "@regression", "@functional", "@search"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openMyUsage(page);
      const before = await selectedAccountLabel(locators);
      const counts = trackOperations(page, [OP_BUDGET_STATUS]);

      const account = await switchToAnotherAccount(page, locators);
      expect(account).not.toBe(before);

      await expect(locators.kpiTile(KPI_ACCOUNT_SPEND)).toContainText(account, { timeout: 15000 });
      await expect(locators.kpiValue(KPI_ACCOUNT_SPEND)).toHaveText(KPI_VALUE_PATTERN);
      await expect(locators.scopeChip).toContainText(account);
      await expect(locators.matrixScopeLabels).toHaveCount(2);
      await expect(locators.matrixRow("Account")).toContainText(account);
      expect(counts[OP_BUDGET_STATUS]).toBe(1);
    }
  );

  test(
    "Nubi My Usage - switch to a different account, then switch to Digests and back to My Usage, verify the picked account survives the remount, the budget status is re-read for it and the account tile still names it",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openMyUsage(page);

      const account = await switchToAnotherAccount(page, locators);
      await expect(locators.kpiTile(KPI_ACCOUNT_SPEND)).toContainText(account, { timeout: 15000 });

      const counts = trackOperations(page, [OP_BUDGET_STATUS]);
      // The pick is modal state (pickedAccountId), not tab state, so leaving the
      // tab must not lose it.
      await remountMyUsage(page, locators);

      await expect(locators.accountSelect).toContainText(account);
      await expect(locators.kpiTile(KPI_ACCOUNT_SPEND)).toContainText(account, { timeout: 15000 });
      await expect(locators.scopeChip).toContainText(account);
      await expect(locators.matrixRow("Account")).toContainText(account);
      expect(counts[OP_BUDGET_STATUS]).toBe(1);
    }
  );

  test(
    "Nubi My Usage - switch to Digests and back to My Usage, verify the tab unmounts, re-reads the budget status on return and renders the same period header with the tenant tile",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openMyUsage(page);
      const periodBefore = ((await locators.usageHeader.innerText()) ?? "").trim();
      const counts = trackOperations(page, [OP_BUDGET_STATUS]);

      await remountMyUsage(page, locators);

      await expect(locators.myUsageTab).toHaveAttribute("aria-selected", "true");
      await expect(locators.usageHeader).toHaveText(periodBefore);
      await expect(locators.kpiTile(KPI_TENANT_SPEND)).toBeVisible();
      expect(counts[OP_BUDGET_STATUS]).toBe(1);
    }
  );

  test(
    "Nubi My Usage - switch the top-level group to Memory and back to Insights, verify Insights keeps My Usage as its selected sub-tab, the usage view re-reads the budget status and the tenant tile is back",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openMyUsage(page);
      const counts = trackOperations(page, [OP_BUDGET_STATUS]);

      await locators.memoryGroupTab.click();
      await locators.usageHeader.waitFor({ state: "detached", timeout: 30000 });
      await expect(locators.myUsageTab).toHaveCount(0);

      // BCortexModal keeps each group's sub-tab in state until the modal closes,
      // so returning to Insights must land on My Usage without another click.
      await locators.insightsGroupTab.click();
      await expect(locators.myUsageTab).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
      await waitForUsageLoaded(locators);
      await expect(locators.kpiTile(KPI_TENANT_SPEND)).toBeVisible();
      await expect(locators.matrixEventAnalysisHeader).toBeVisible();
      await expect(locators.matrixRow("Tenant")).toContainText(/\/|No limits|—/);
      expect(counts[OP_BUDGET_STATUS]).toBe(1);
    }
  );
});
