// Not for OSS

import { existsSync } from "fs";
import { test, expect, Page } from "@playwright/test";
import { LoginPage } from "../../../pages/LoginPage";
import { NubiLocators } from "../nubiLocators";
import { waitForGraphQLAndValidate } from "../../utils/GraphQLNetworkWatcher";
import { AUTH_STATE_PATH } from "../../utils/paths";
import { CHAT_ACCOUNT, FUNCTIONS_TAB_HINT, ensureLlmFunctionFeatureEnabled, typeCallAndExpectOffered } from "./functionsHelper";

const OP_CREATE = "CreateAiFunction";
const OP_UPDATE = "AiEditFunction";
const OP_DELETE = "AiDeleteFunction";

const QA_FUNCTION_PREFIX = "qa_";

function randomName(): string {
  return `${QA_FUNCTION_PREFIX}${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
}

async function openFunctionsTab(page: Page, accountName: string = CHAT_ACCOUNT): Promise<NubiLocators> {
  const loginPage = new LoginPage(page);
  const locators = new NubiLocators(page);
  await loginPage.doFullLogin();
  await locators.openPanel();
  await openFunctionsTabFromPanel(locators, accountName);
  return locators;
}

const CHAT_REFRESH_MAX_ATTEMPTS = 5;
const CHAT_REFRESH_RETRY_INTERVAL_MS = 2000;

async function reopenChatWithFreshFunctions(page: Page, locators: NubiLocators, functionName: string): Promise<void> {
  let attempt = 0;
  await expect(async () => {
    attempt++;
    await page.reload({ waitUntil: "domcontentloaded" });
    // Absence here is the normal "panel needs reopening" branch handled right below, not a failure.
    const chatIsUp = await locators.chatTextbox
      .waitFor({ state: "visible", timeout: 30000 })
      .then(() => true)
      .catch(() => false);
    if (!chatIsUp) {
      await locators.openPanel();
      await expect(locators.chatTextbox).toBeVisible({ timeout: 30000 });
    }

    await locators.chatTextbox.fill("/call ");
    await expect(
      locators.callSuggestion(functionName),
      `'${functionName}' not offered after reload ${attempt}/${CHAT_REFRESH_MAX_ATTEMPTS}`
    ).toBeVisible({ timeout: 8000 });
  }).toPass({
    timeout: 150000,
    intervals: Array(CHAT_REFRESH_MAX_ATTEMPTS - 1).fill(CHAT_REFRESH_RETRY_INTERVAL_MS),
  });
}

// b-Cortex > Knowledge > My Functions. Knowledge is not b-Cortex's default landing
// group and My Functions is not its default sub-tab, so this takes two clicks.
async function openFunctionsTabFromPanel(locators: NubiLocators, accountName: string = CHAT_ACCOUNT): Promise<void> {
  await locators.openBCortex(locators.knowledgeGroupTab);
  await locators.knowledgeGroupTab.click();
  await locators.functionsTab.waitFor({ state: "visible", timeout: 20000 }).catch((e) => {
    throw new Error(`${FUNCTIONS_TAB_HINT} Underlying failure: ${e}`);
  });
  await locators.functionsTab.click();
  await locators.selectAdminAccount(accountName);
  await locators.createFunctionBtn.waitFor({ state: "visible", timeout: 15000 });
}

interface CreateOpts {
  name: string;
  description: string;
  prompt: string;
  draft?: boolean;
  defaults?: Record<string, string>;
}

async function createFunction(page: Page, locators: NubiLocators, opts: CreateOpts): Promise<void> {
  await locators.createFunctionBtn.click();
  await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
  await locators.functionNameInput.fill(opts.name);
  await locators.functionDescriptionInput.fill(opts.description);
  await locators.functionPromptInput.fill(opts.prompt);

  if (opts.defaults) {
    for (const [variable, value] of Object.entries(opts.defaults)) {
      await page.getByPlaceholder(`Default value for ${variable}`).fill(value);
    }
  }
  if (opts.draft) {
    await locators.functionStatusSelect.click();
    await locators.functionStatusDraftOption.click({ timeout: 5000 });
  }

  await waitForGraphQLAndValidate(
    page,
    async () => {
      await locators.saveFunctionBtn.click();
      await expect(locators.functionCreatedMessage).toBeVisible({ timeout: 15000 });
    },
    { testName: `Create function ${opts.name}`, operationNames: OP_CREATE, timeoutMs: 30000 }
  );
}

async function openEditForm(page: Page, locators: NubiLocators, name: string): Promise<void> {
  await locators.searchFunctionInput.clear();
  await locators.searchFunctionInput.fill(name);
  await expect(page.getByText(name)).toBeVisible({ timeout: 15000 });
  await locators.functionMoreActionsBtn.click();
  await locators.editFunctionMenuItem.waitFor({ state: "visible", timeout: 10000 });
  await locators.editFunctionMenuItem.click();
}

async function deleteFunction(page: Page, locators: NubiLocators, name: string, validate = true): Promise<void> {
  const run = async () => {
    await locators.searchFunctionInput.clear();
    await locators.searchFunctionInput.fill(name);
    await expect(page.getByText(name)).toBeVisible({ timeout: validate ? 15000 : 3000 });
    await locators.functionMoreActionsBtn.click();
    await locators.deleteFunctionMenuItem.waitFor({ state: "visible", timeout: 10000 });
    await locators.deleteFunctionMenuItem.click();
    await locators.confirmDeleteFunctionBtn.waitFor({ state: "visible", timeout: 10000 });

    if (validate) {
      await waitForGraphQLAndValidate(
        page,
        async () => {
          await locators.confirmDeleteFunctionBtn.click();
          await expect(locators.functionDeletedMessage).toBeVisible({ timeout: 15000 });
        },
        { testName: `Delete function ${name}`, operationNames: OP_DELETE, timeoutMs: 30000 }
      );
    } else {
      await locators.confirmDeleteFunctionBtn.click();
      await expect(locators.functionDeletedMessage).toBeVisible({ timeout: 15000 });
    }
  };

  if (validate) {
    await run();
  } else {
    // Best-effort cleanup: another test or the final sweep may already have deleted this
    // function, so a failed cleanup delete must not fail the test that called it.
    await run().catch((e) => console.warn(`[cleanup] delete '${name}' skipped: ${e}`));
  }
}

const CLEANUP_MAX_ATTEMPTS = 50;

async function cleanupOrphanedQaFunctions(page: Page, locators: NubiLocators): Promise<void> {
  await locators.searchFunctionInput.clear();
  await locators.searchFunctionInput.fill(QA_FUNCTION_PREFIX);

  const emptyState = page.getByText(/No Data Available/i);
  let cleaned = 0;

  for (let attempt = 0; attempt < CLEANUP_MAX_ATTEMPTS; attempt++) {
    const row = locators.functionRowsMatching(QA_FUNCTION_PREFIX).first();
    await expect(row.or(emptyState)).toBeVisible({ timeout: 10000 });
    // The empty state winning the race above is the normal "nothing left to clean" exit.
    if (!(await row.isVisible().catch(() => false))) break;

    // Row text is only for the log line below; a parse miss must not block the delete itself.
    const rowText = (await row.textContent().catch(() => "")) || "";
    const leftoverName = rowText.match(new RegExp(`${QA_FUNCTION_PREFIX}\\d+_[a-z0-9]+`))?.[0] ?? rowText.trim();

    await locators.functionRowMoreActionsBtn(row).dispatchEvent("click");
    await locators.deleteFunctionMenuItem.waitFor({ state: "visible", timeout: 10000 });
    await locators.deleteFunctionMenuItem.click();
    await locators.confirmDeleteFunctionBtn.waitFor({ state: "visible", timeout: 10000 });
    await locators.confirmDeleteFunctionBtn.click();
    await expect(locators.functionDeletedMessage).toBeVisible({ timeout: 15000 });

    cleaned++;
    console.log(`[cleanup-sweep] deleted leftover '${leftoverName}'`);
  }

  if (cleaned === 0) {
    console.log(`[cleanup-sweep] no leftover '${QA_FUNCTION_PREFIX}*' functions found — every test cleaned up after itself.`);
  } else {
    console.log(`[cleanup-sweep] deleted ${cleaned} leftover '${QA_FUNCTION_PREFIX}*' function(s).`);
  }
}

function trackMutation(page: Page, opName: string): { count: number } {
  const state = { count: 0 };
  page.on("request", (req) => {
    if (req.method() !== "POST" || !req.url().includes("api/graphql")) return;
    const postData = req.postData();
    if (!postData) return;
    try {
      const payload = JSON.parse(postData);
      const operations = Array.isArray(payload) ? payload : [payload];
      if (operations.some((op: { operationName?: string }) => op.operationName === opName)) {
        state.count += 1;
      }
    } catch {
      if (postData.includes(opName)) state.count += 1;
    }
  });
  return state;
}

test.describe("Nubi Functions Tab", () => {
  test.beforeAll(async ({ browser, baseURL }) => {
    test.setTimeout(300000);
    await ensureLlmFunctionFeatureEnabled(browser, baseURL);
  });

  test.afterAll(async ({ browser, baseURL }) => {
    test.setTimeout(180000);
    const context = await browser.newContext({
      baseURL,
      storageState: existsSync(AUTH_STATE_PATH) ? AUTH_STATE_PATH : undefined,
    });
    const page = await context.newPage();
    try {
      const locators = await openFunctionsTab(page);
      await cleanupOrphanedQaFunctions(page, locators);
    } finally {
      await page.close();
      await context.close();
    }
  });

  test.describe("CRUD", () => {
    test(
      "Nubi Functions - create a function in b-Cortex and verify it appears in the function list",
      { tag: ["@dev", "@test", "@regression", "@functional", "@crud"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function created by automation for the create test.",
          prompt: "Summarize recent errors in the cluster.",
        });
        console.log(`[create] '${name}' created.`);

        await locators.searchFunctionInput.click();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).toBeVisible({ timeout: 20000 });
        console.log(`[create] '${name}' verified in list.`);

        await deleteFunction(page, locators, name, false);
      }
    );

    test(
      "Nubi Functions - edit a function's description and verify AiEditFunction persists the change",
      { tag: ["@dev", "@test", "@regression", "@functional", "@crud"] },
      async ({ page }) => {
        test.setTimeout(150000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Original description.",
          prompt: "Original prompt body.",
        });

        await openEditForm(page, locators, name);
        await locators.functionDescriptionInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.functionDescriptionInput.clear();
        await locators.functionDescriptionInput.fill("Updated description by automation.");

        await waitForGraphQLAndValidate(
          page,
          async () => {
            await locators.updateFunctionBtn.click();
            await expect(locators.functionUpdatedMessage).toBeVisible({ timeout: 15000 });
          },
          { testName: "Nubi Functions - update function description", operationNames: OP_UPDATE, timeoutMs: 30000 }
        );
        console.log(`[update] '${name}' updated.`);

        await deleteFunction(page, locators, name, false);
      }
    );

    test(
      "Nubi Functions - delete a function and verify AiDeleteFunction removes it from the list",
      { tag: ["@dev", "@test", "@regression", "@functional", "@crud"] },
      async ({ page }) => {
        test.setTimeout(150000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function to be deleted.",
          prompt: "Prompt body for deletion test.",
        });

        await deleteFunction(page, locators, name, true);
        console.log(`[delete] '${name}' deleted.`);

        await locators.searchFunctionInput.clear();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).not.toBeVisible({ timeout: 10000 });
        console.log(`[delete] '${name}' confirmed removed.`);
      }
    );

    test(
      "Nubi Functions - search the function list by name and verify a non-matching search hides it",
      { tag: ["@dev", "@test", "@regression", "@functional", "@search", "@crud"] },
      async ({ page }) => {
        test.setTimeout(150000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function for the search test.",
          prompt: "Prompt body for search test.",
        });

        await locators.searchFunctionInput.click();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).toBeVisible({ timeout: 15000 });
        console.log(`[search] match shows '${name}'.`);

        await locators.searchFunctionInput.clear();
        await locators.searchFunctionInput.fill(`${name}_zzz_no_match`);
        await expect(page.getByText(name)).not.toBeVisible({ timeout: 10000 });
        console.log(`[search] no-match hides '${name}'.`);

        await deleteFunction(page, locators, name, false);
      }
    );
  });

  test.describe("Validation", () => {
    test(
      "Nubi Functions - reject an invalid function name and verify no CreateAiFunction mutation is sent",
      { tag: ["@dev", "@test", "@regression", "@negative", "@validation"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);
        const mutations = trackMutation(page, OP_CREATE);

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });

        await locators.functionNameInput.fill("bad-name");
        await expect(page.getByText("Name can only contain letters, numbers, and underscores.")).toBeVisible({ timeout: 10000 });

        await locators.functionNameInput.fill("2function");
        await expect(page.getByText("Name must start with a letter (a-z or A-Z).")).toBeVisible({ timeout: 10000 });

        await locators.functionNameInput.fill("ab");
        await expect(page.getByText("Name must be at least 3 characters long.")).toBeVisible({ timeout: 10000 });

        expect(mutations.count).toBe(0);
        console.log("[reject-name] invalid names rejected; zero mutations.");
      }
    );

    test(
      "Nubi Functions - reject empty description and prompt and verify no CreateAiFunction mutation is sent",
      { tag: ["@dev", "@test", "@regression", "@negative", "@validation"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);
        const mutations = trackMutation(page, OP_CREATE);

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.functionNameInput.fill("valid_function_name");

        await locators.saveFunctionBtn.click();
        await expect(page.getByText("Description is required")).toBeVisible({ timeout: 10000 });
        await expect(page.getByText("Prompt is required")).toBeVisible({ timeout: 10000 });

        expect(mutations.count).toBe(0);
        console.log("[reject-required] empty fields rejected; zero mutations.");
      }
    );

    test(
      "Nubi Functions - reject a description over 200 words and verify no CreateAiFunction mutation is sent",
      { tag: ["@dev", "@test", "@regression", "@negative", "@validation"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);
        const mutations = trackMutation(page, OP_CREATE);

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.functionNameInput.fill("valid_function_name");
        await locators.functionDescriptionInput.fill(Array(250).fill("word").join(" "));
        await locators.functionPromptInput.fill("A valid prompt body.");

        await locators.saveFunctionBtn.click();
        await page.waitForTimeout(1500);
        await expect(locators.functionNameInput).toBeVisible();

        expect(mutations.count).toBe(0);
        console.log("[reject-description] over-200-word description blocked; zero mutations.");
      }
    );

    test(
      "Nubi Functions - reject an invalid variable name in the prompt and verify no CreateAiFunction mutation is sent",
      { tag: ["@dev", "@test", "@regression", "@negative", "@validation"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);
        const mutations = trackMutation(page, OP_CREATE);

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.functionPromptInput.fill("Inspect logs for <bad-var> now.");
        await expect(page.getByText(/Invalid variable names:/)).toBeVisible({ timeout: 10000 });

        expect(mutations.count).toBe(0);
        console.log("[reject-variable] invalid variable name rejected; zero mutations.");
      }
    );
  });

  test.describe("Variables & Status", () => {
    test(
      "Nubi Functions - detect <variable> chips from the prompt, save a default value, and verify it persists on reopen",
      { tag: ["@dev", "@test", "@regression", "@functional", "@crud"] },
      async ({ page }) => {
        test.setTimeout(150000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.functionNameInput.fill(name);
        await locators.functionDescriptionInput.fill("Function with two variables.");
        await locators.functionPromptInput.fill("Analyze logs for the <service_name> service in the <namespace> namespace.");

        await expect(page.getByText("<service_name>", { exact: true })).toBeVisible({ timeout: 10000 });
        await expect(page.getByText("<namespace>", { exact: true })).toBeVisible({ timeout: 10000 });
        await expect(locators.variableDefaultsHeading).toBeVisible({ timeout: 10000 });
        await expect(page.getByPlaceholder("Default value for service_name")).toBeVisible({ timeout: 10000 });
        await expect(page.getByPlaceholder("Default value for namespace")).toBeVisible({ timeout: 10000 });
        await page.getByPlaceholder("Default value for service_name").fill("payments");
        console.log("[variables] chips + defaults table rendered.");

        await waitForGraphQLAndValidate(
          page,
          async () => {
            await locators.saveFunctionBtn.click();
            await expect(locators.functionCreatedMessage).toBeVisible({ timeout: 15000 });
          },
          { testName: "Nubi Functions - create function with variables", operationNames: OP_CREATE, timeoutMs: 30000 }
        );

        await locators.searchFunctionInput.click();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).toBeVisible({ timeout: 20000 });
        await expect(page.getByText(/2 vars/i)).toBeVisible({ timeout: 10000 });

        await openEditForm(page, locators, name);
        const def = page.getByPlaceholder("Default value for service_name");
        await expect(def).toBeVisible({ timeout: 10000 });
        await expect(def).toHaveValue("payments");
        console.log("[variables] default value persisted across save/reopen.");
        await locators.functionCancelBtn.click();

        await deleteFunction(page, locators, name, false);
      }
    );

    test(
      "Nubi Functions - create a function as Draft, change it to Active, and verify AiEditFunction persists the status",
      { tag: ["@dev", "@test", "@regression", "@functional", "@crud", "@quarantine"] },
      async ({ page }) => {
        test.setTimeout(150000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function created in draft status.",
          prompt: "Prompt body for status test.",
          draft: true,
        });

        await locators.searchFunctionInput.click();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).toBeVisible({ timeout: 20000 });
        await expect(page.getByText("draft", { exact: true })).toBeVisible({ timeout: 10000 });
        console.log("[status] created as Draft.");

        await openEditForm(page, locators, name);
        await locators.functionStatusSelect.click();
        await locators.functionStatusActiveOption.click({ timeout: 5000 });

        await waitForGraphQLAndValidate(
          page,
          async () => {
            await locators.updateFunctionBtn.click();
            await expect(locators.functionUpdatedMessage).toBeVisible({ timeout: 15000 });
          },
          { testName: "Nubi Functions - change function status draft to active", operationNames: OP_UPDATE, timeoutMs: 30000 }
        );

        await locators.searchFunctionInput.clear();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).toBeVisible({ timeout: 10000 });
        await expect(page.getByText("active", { exact: true })).toBeVisible({ timeout: 10000 });
        console.log("[status] changed to Active.");

        await deleteFunction(page, locators, name, false);
      }
    );
  });

  test.describe("Form features", () => {
    test(
      "Nubi Functions - click Rewrite Prompt with an empty prompt and verify the required-prompt error",
      { tag: ["@dev", "@test", "@regression", "@negative", "@validation"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.rewritePromptBtn.click();
        await expect(locators.rewritePromptEmptyError).toBeVisible({ timeout: 10000 });
        console.log("[rewrite-prompt] empty-prompt rewrite guard shows error.");
      }
    );

    test(
      "Nubi Functions - open View Agent List and verify the modal opens with a searchable agent list",
      { tag: ["@dev", "@test", "@sanity", "@functional"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.viewAgentListBtn.click();
        await expect(locators.agentListModalTitle).toBeVisible({ timeout: 10000 });
        await expect(locators.agentListSearchInput).toBeVisible({ timeout: 10000 });
        await locators.agentListSearchInput.fill("zzz_no_such_agent");
        console.log("[agent-list] Available Agents modal opened and searchable.");
      }
    );

    test(
      "Nubi Functions - click Cancel on a new function and verify it is discarded without sending any mutation",
      { tag: ["@dev", "@test", "@regression", "@negative"] },
      async ({ page }) => {
        test.setTimeout(120000);
        const locators = await openFunctionsTab(page);
        const mutations = trackMutation(page, OP_CREATE);
        const name = randomName();

        await locators.createFunctionBtn.click();
        await locators.functionNameInput.waitFor({ state: "visible", timeout: 10000 });
        await locators.functionNameInput.fill(name);
        await locators.functionDescriptionInput.fill("This function should never be saved.");
        await locators.functionPromptInput.fill("A prompt that gets discarded.");
        await locators.functionCancelBtn.click();

        await expect(locators.functionNameInput).not.toBeVisible({ timeout: 10000 });
        await locators.searchFunctionInput.click();
        await locators.searchFunctionInput.fill(name);
        await expect(page.getByText(name)).not.toBeVisible({ timeout: 10000 });

        expect(mutations.count).toBe(0);
        console.log("[cancel] cancel discarded the function; zero mutations.");
      }
    );
  });

  test.describe("Usage in Nubi", () => {
    test(
      "Nubi Functions - create a function in b-Cortex, reload, type /call in the Nubi chat, verify the function is offered as a suggestion and fills the box as /call <name>",
      { tag: ["@dev", "@test", "@regression", "@functional"] },
      async ({ page }) => {
        test.setTimeout(180000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function called from the Nubi chat by automation.",
          prompt: "Summarize the recent errors for this cluster.",
        });

        await reopenChatWithFreshFunctions(page, locators, name);
        console.log(`[call-basic] '${name}' offered in the /call list.`);

        await locators.callSuggestion(name).click();
        await expect(locators.chatTextbox).toHaveValue(`/call ${name}`, { timeout: 15000 });
        console.log(`[call-basic] '${name}' composed into the chat box.`);

        await openFunctionsTabFromPanel(locators);
        await deleteFunction(page, locators, name, false);
      }
    );

    test(
      "Nubi Functions - create a function whose prompt carries two variables with a saved default, pick it from /call in the Nubi chat, verify the composed call carries both variables and the default value",
      { tag: ["@dev", "@test", "@regression", "@functional"] },
      async ({ page }) => {
        test.setTimeout(180000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function with variables called from the Nubi chat.",
          prompt: "Analyze logs for the <service_name> service in the <namespace> namespace.",
          defaults: { service_name: "payments" },
        });

        await reopenChatWithFreshFunctions(page, locators, name);
        await locators.callSuggestion(name).click();

        await expect(locators.chatTextbox).toHaveValue(`/call ${name} service_name="payments" namespace=""`, { timeout: 15000 });
        console.log(`[call-variables] '${name}' composed with its variables and saved default.`);

        await openFunctionsTabFromPanel(locators);
        await deleteFunction(page, locators, name, false);
      }
    );

    test(
      "Nubi Functions - create a function, open the Nubi chat, verify /call offers it, type a prefix that matches no function, verify it drops out of the list, then type its own prefix and verify it comes back",
      { tag: ["@dev", "@test", "@regression", "@negative"] },
      async ({ page }) => {
        test.setTimeout(180000);
        const locators = await openFunctionsTab(page);
        const name = randomName();

        await createFunction(page, locators, {
          name,
          description: "Function used for the /call prefix filter test.",
          prompt: "Report the pod restarts in this namespace.",
        });

        await reopenChatWithFreshFunctions(page, locators, name);
        console.log(`[call-filter] '${name}' offered before filtering.`);

        await locators.chatTextbox.fill("/call zzz");
        await expect(locators.callSuggestion(name)).toBeHidden({ timeout: 15000 });
        console.log(`[call-filter] non-matching prefix drops '${name}'.`);

        await typeCallAndExpectOffered(locators, `/call ${name.slice(0, 6)}`, name);
        console.log(`[call-filter] own prefix offers '${name}' again.`);

        await openFunctionsTabFromPanel(locators);
        await deleteFunction(page, locators, name, false);
      }
    );
  });
});
