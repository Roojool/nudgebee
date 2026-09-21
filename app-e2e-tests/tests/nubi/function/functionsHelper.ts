// Not for OSS

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "fs";
import { Browser, Locator, Page, expect } from "@playwright/test";
import { LoginPage } from "../../../pages/LoginPage";
import {
  AUTH_STATE_PATH,
  LLM_FUNCTION_FLAG_VERIFIED_PATH,
  AUTH_STATE_DIR,
} from "../../utils/paths";
import { NubiLocators } from "../nubiLocators";
import { TenantSettingsLocators } from "../../admin/TenantSettings/tenantSettingsLocators";
import {
  discardUnsavedEdits,
  openTenantSettings,
  toast,
} from "../../admin/TenantSettings/tenantSettingsHelper";
import {
  FEATURE_LLM_FUNCTION,
  FEATURE_LLM_FUNCTION_DESCRIPTION,
  SAVE_SUCCESS_TEXT,
} from "../../admin/TenantSettings/tenantSettingsConstants";

export const CHAT_ACCOUNT = process.env.CLUSTER || "";
if (!CHAT_ACCOUNT) {
  throw new Error("CLUSTER is not set — add it to .env / .env.dev");
}

export const FUNCTIONS_TAB_HINT =
  "The Functions tab is not in the AI & Tools modal. aiToolsConfig.js gates it on the " +
  "LLM_FUNCTION tenant feature flag, so the tab is dropped from the strip whenever the flag " +
  "is off — check Admin -> Tenant Settings -> Features before reading this as a Functions " +
  "module failure. ensureLlmFunctionFeatureEnabled() is meant to have settled this already.";

function featureToggle(locators: TenantSettingsLocators): Locator {
  return locators
    .featureToggle(FEATURE_LLM_FUNCTION)
    .or(locators.featureToggleByDescription(FEATURE_LLM_FUNCTION_DESCRIPTION))
    .first();
}

const FLAG_VERIFIED_TTL_MS = 60 * 60 * 1000;

function flagVerifiedRecently(): boolean {
  if (!existsSync(LLM_FUNCTION_FLAG_VERIFIED_PATH)) return false;
  const writtenAt = Number(
    readFileSync(LLM_FUNCTION_FLAG_VERIFIED_PATH, "utf-8")
  );
  return (
    Number.isFinite(writtenAt) && Date.now() - writtenAt < FLAG_VERIFIED_TTL_MS
  );
}

function markFlagVerified(): void {
  mkdirSync(AUTH_STATE_DIR, { recursive: true });
  writeFileSync(LLM_FUNCTION_FLAG_VERIFIED_PATH, String(Date.now()));
}

export async function ensureLlmFunctionFeatureEnabled(
  browser: Browser,
  baseURL?: string
): Promise<void> {
  if (flagVerifiedRecently()) {
    console.log(
      `"${FEATURE_LLM_FUNCTION}" was already verified earlier in this run — skipping the admin check`
    );
    return;
  }

  const context = await browser.newContext({
    baseURL,
    storageState: existsSync(AUTH_STATE_PATH) ? AUTH_STATE_PATH : undefined,
  });
  const page = await context.newPage();
  try {
    await new LoginPage(page).doFullLogin();
    const ts = await openTenantSettings(page);
    await ts.openTab(ts.featuresTab);
    await expect(ts.featuresTable).toBeVisible({ timeout: 60000 });

    const toggle = featureToggle(ts);
    await expect(
      toggle,
      `"${FEATURE_LLM_FUNCTION}" is not on the Features table — the feature catalog may have renamed it`
    ).toBeVisible({ timeout: 30000 });

    if (await toggle.isChecked()) {
      console.log(
        `"${FEATURE_LLM_FUNCTION}" is already enabled — nothing to do`
      );
      markFlagVerified();
      return;
    }

    if (!(await ts.canEdit())) {
      throw new Error(
        `"${FEATURE_LLM_FUNCTION}" is disabled and this run's user lacks tenants:Write to enable it — ` +
          "grant the flag on this tenant directly, or point the suite at a user who can."
      );
    }

    const attempts = 3;
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (!(await featureToggle(ts).isChecked())) {
        await featureToggle(ts).click();
        await expect(featureToggle(ts)).toBeChecked();
        // Cope with both shapes of the Features tab: this PR applies the toggle on change
        // (no Save), while the currently-deployed build still needs Save. Take whichever is present.
        const save = ts.saveButton();
        if ((await save.count()) > 0) {
          await save.click();
          await expect(toast(page, SAVE_SUCCESS_TEXT)).toBeVisible({
            timeout: 60000,
          });
        } else {
          await expect(featureToggle(ts)).toBeEnabled({ timeout: 60000 });
        }
      }

      const reloaded = await discardUnsavedEdits(page);
      await reloaded.openTab(reloaded.featuresTab);
      await expect(reloaded.featuresTable).toBeVisible({ timeout: 60000 });
      if (await featureToggle(reloaded).isChecked()) {
        console.log(
          `"${FEATURE_LLM_FUNCTION}" enabled and verified on attempt ${attempt}/${attempts}`
        );
        markFlagVerified();
        return;
      }
      console.log(
        `"${FEATURE_LLM_FUNCTION}" did not read back as enabled on attempt ${attempt}/${attempts}, retrying...`
      );
    }

    throw new Error(
      `Failed to enable "${FEATURE_LLM_FUNCTION}" after ${attempts} attempts`
    );
  } finally {
    await page.close();
    await context.close();
  }
}

export async function openNubiChat(page: Page): Promise<NubiLocators> {
  const locators = new NubiLocators(page);
  await new LoginPage(page).doFullLogin();
  await locators.openPanel();
  await expect(locators.chatTextbox).toBeVisible({ timeout: 30000 });
  return locators;
}

export async function typeCallAndExpectOffered(
  locators: NubiLocators,
  text: string,
  functionName: string
): Promise<void> {
  await expect(async () => {
    await locators.chatTextbox.fill("");
    await locators.chatTextbox.fill(text);
    await expect(locators.callSuggestion(functionName)).toBeVisible({
      timeout: 5000,
    });
  }).toPass({ timeout: 90000, intervals: [2000, 3000, 5000] });
}
