import { test, expect } from "@playwright/test";
import { WorkflowLocators } from "../workflowlocators";
import { loginAndNavigateToNewWorkflow, pasteAndApplyWorkflowJson, saveNewWorkflow } from "../workflowHelper";
import { waitForGraphQLAndValidate } from "../../utils/GraphQLNetworkWatcher";
import { openAutomations, openSettingsModal, deleteIfExists, leaveBuilder } from "./automationsHelper";

// The AI assistant actually running the automation from chat is not covered here - it would execute against real infrastructure, which the QA no-harmful-runs rule disallows.

// Fixed rather than timestamped: a stable name makes the automation recognisable
// in the shared tenant's listing across runs, at the cost of needing the pre-run
// delete below since a previous interrupted run can leave one behind.
const WIRING_AUTOMATION_NAME = "qa-e2e-automation-settings-wiring";
const PERSIST_AUTOMATION_NAME = "qa-e2e-automation-settings-persist";

// crypto.hash (runbook-server/internal/tasks/crypto/hash_task.go): pure computation,
// no infrastructure side effect, safe to embed in a throwaway automation.
function manualCryptoHashWorkflowJson(name: string) {
  return {
    name,
    definition: {
      version: "v1",
      timeout: "",
      inputs: [],
      output: {},
      tasks: [{ id: "crypto_hash", type: "crypto.hash", params: { data: "qa-e2e-probe", algorithm: "sha256" }, timeout: "1m" }],
      triggers: [{ type: "manual", params: {} }],
      retry_policy: { maximum_attempts: 3, initial_interval: "1s", maximum_interval: "", backoff_coefficient: 2 },
    },
    tags: {},
    status: "ACTIVE",
  };
}

test(
  "Automation Settings - AI Assistant: create an automation, verify Run without asking the user to confirm stays disabled and unchecked until Allow the AI assistant to run this automation is on, and clears itself the moment that is turned back off",
  { tag: ["@dev", "@test", "@regression", "@validation", "@functional", "@nb-38011", "@oss"] },
  async ({ page }) => {
    test.setTimeout(180000);

    const automationName = WIRING_AUTOMATION_NAME;
    const locators = await openAutomations(page);
    // A prior interrupted run can leave this fixed name behind - clear it before
    // creating, so the automation this test opens is always the one it just made.
    await deleteIfExists(page, locators, automationName);

    const workflowLocators = new WorkflowLocators(page);
    await loginAndNavigateToNewWorkflow(page, workflowLocators);
    await pasteAndApplyWorkflowJson(page, workflowLocators, manualCryptoHashWorkflowJson(automationName));
    await saveNewWorkflow(page, workflowLocators, automationName);
    // WorkflowBuilderNotebook's create flow replaces the canvas's nodes ~500ms
    // after the redirect via a background reload (same trigger node before and
    // after, so nothing observable to wait on) - opening Settings before that
    // lands can read the trigger as missing and leave the AI-invocable checkbox
    // permanently disabled for the rest of the test.
    await page.waitForTimeout(3000);

    try {
      await openSettingsModal(locators);

      // Recorded before any edit: Cancel discards local changes, so reopening must show this again.
      const originalAllowChecked = await locators.aiInvocableCheckbox.isChecked();

      await test.step("Allow the AI assistant to run this automation needs a description before it can be turned on", async () => {
        // WorkflowSettingsModal's remaining precondition (manual trigger is already satisfied) is a non-empty description.
        if (!(await locators.aiInvocableCheckbox.isEnabled())) {
          await locators.llmDescriptionInput.fill("e2e probe description - never saved");
          await expect(locators.aiInvocableCheckbox).toBeEnabled({ timeout: 15000 });
        }
      });

      await test.step("With Allow the AI assistant off, Run without asking the user to confirm is disabled and unchecked", async () => {
        if (await locators.aiInvocableCheckbox.isChecked()) {
          await locators.aiInvocableCheckbox.uncheck();
        }
        await expect(locators.aiAutoApproveCheckbox).toBeDisabled();
        await expect(locators.aiAutoApproveCheckbox).not.toBeChecked();
      });

      await test.step("Turning Allow the AI assistant on enables Run without asking the user to confirm", async () => {
        // On a slower runner the checkbox's disabled prop can keep flipping for a
        // few seconds after becoming enabled once (WorkflowSettingsModal recomputes
        // it off a freshly-created automation's still-settling trigger/description
        // state) - retry the whole check rather than trusting a single enabled wait.
        await expect(async () => {
          await locators.aiInvocableCheckbox.check();
          await expect(locators.aiInvocableCheckbox).toBeChecked({ timeout: 2000 });
        }).toPass({ timeout: 30000 });
        await expect(locators.aiAutoApproveCheckbox).toBeEnabled();

        await locators.aiAutoApproveCheckbox.check();
        await expect(locators.aiAutoApproveCheckbox).toBeChecked();
      });

      await test.step("Turning Allow the AI assistant back off clears and disables Run without asking the user to confirm on its own", async () => {
        await locators.aiInvocableCheckbox.uncheck();
        await expect(locators.aiAutoApproveCheckbox).not.toBeChecked();
        await expect(locators.aiAutoApproveCheckbox).toBeDisabled();
      });

      await test.step("Cancel discards every local edit above - reopening Settings shows the automation's original saved state", async () => {
        await locators.settingsCancelBtn.click();
        await expect(locators.settingsModal).toBeHidden({ timeout: 15000 });
        await expect(locators.toast(/updated successfully/i)).toHaveCount(0);

        await openSettingsModal(locators);
        const reopenedAllowChecked = await locators.aiInvocableCheckbox.isChecked();
        expect(reopenedAllowChecked).toBe(originalAllowChecked);
        await locators.settingsCancelBtn.click();
        await expect(locators.settingsModal).toBeHidden({ timeout: 15000 });
      });

      await leaveBuilder(page, locators);
    } finally {
      await deleteIfExists(page, locators, automationName);
    }
  }
);

test(
  "Automation Settings - AI Assistant: create an automation, turn on Allow the AI assistant to run this automation and Run without asking the user to confirm, save and publish, verify both persist after a reload",
  { tag: ["@dev", "@test", "@regression", "@crud", "@nb-38011", "@oss"] },
  async ({ page }) => {
    test.setTimeout(180000);

    const automationName = PERSIST_AUTOMATION_NAME;
    const locators = await openAutomations(page);
    // A prior interrupted run can leave this fixed name behind - clear it before
    // creating, so the automation this test opens is always the one it just made.
    await deleteIfExists(page, locators, automationName);

    const workflowLocators = new WorkflowLocators(page);
    await loginAndNavigateToNewWorkflow(page, workflowLocators);
    await pasteAndApplyWorkflowJson(page, workflowLocators, manualCryptoHashWorkflowJson(automationName));
    await saveNewWorkflow(page, workflowLocators, automationName);
    // WorkflowBuilderNotebook's create flow replaces the canvas's nodes ~500ms
    // after the redirect via a background reload (same trigger node before and
    // after, so nothing observable to wait on) - opening Settings before that
    // lands can read the trigger as missing and leave the AI-invocable checkbox
    // permanently disabled for the rest of the test.
    await page.waitForTimeout(3000);

    const probeDescription = `e2e-ai-auto-approve-probe-${Date.now()}`;

    try {
      await openSettingsModal(locators);

      await test.step("Turn on both checkboxes and apply", async () => {
        await locators.llmDescriptionInput.fill(probeDescription);
        // On a slower runner the checkbox's disabled prop can keep flipping for a
        // few seconds after becoming enabled once (WorkflowSettingsModal recomputes
        // it off a freshly-created automation's still-settling trigger/description
        // state) - retry the whole check rather than trusting a single enabled wait.
        await expect(async () => {
          await locators.aiInvocableCheckbox.check();
          await expect(locators.aiInvocableCheckbox).toBeChecked({ timeout: 2000 });
        }).toPass({ timeout: 30000 });
        await expect(locators.aiAutoApproveCheckbox).toBeEnabled();
        await locators.aiAutoApproveCheckbox.check();

        await locators.settingsApplyBtn.click();
        await expect(locators.settingsModal).toBeHidden({ timeout: 15000 });
      });

      await test.step("Publish snapshots the draft as the live version", async () => {
        // A brand-new automation has no live version yet, so Publish is already
        // enabled without a separate Save draft - and Publish itself saves first.
        await expect(locators.workflowPublishBtn).toBeEnabled({ timeout: 30000 });
        await locators.workflowPublishBtn.click();
        await expect(locators.publishDialog).toBeVisible({ timeout: 15000 });

        await waitForGraphQLAndValidate(
          page,
          async () => {
            await locators.publishConfirmBtn.click();
          },
          {
            testName: "AutomationAISettings - PublishWorkflowVersion",
            operationNames: ["PublishWorkflowVersion"],
            timeoutMs: 30000,
            checkDataErrors: true,
          }
        );
        await expect(locators.toast(/Published/)).toBeVisible({ timeout: 15000 });
      });

      await test.step("A reload re-fetches the automation from the server - both checkboxes and the description are still there", async () => {
        await page.reload();
        await expect(locators.workflowSettingsBtn).toBeVisible({ timeout: 60000 });
        await openSettingsModal(locators);

        await expect(locators.aiInvocableCheckbox).toBeChecked();
        await expect(locators.aiAutoApproveCheckbox).toBeChecked();
        await expect(locators.llmDescriptionInput).toHaveValue(probeDescription);

        await locators.settingsCancelBtn.click();
        await expect(locators.settingsModal).toBeHidden({ timeout: 15000 });
      });

      await leaveBuilder(page, locators);
    } finally {
      await deleteIfExists(page, locators, automationName);
    }
  }
);
