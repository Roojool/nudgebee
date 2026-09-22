// Not for OSS

import { test, expect } from "@playwright/test";
import { openTaskRunner, selectRunnerAccount, selectTask, setTaskField, readTaskField, runTask, expectRunSucceeded, readRunOutput } from "./taskRunnerHelper";

// #34764: Filter and Transform carry click-to-fill examples and a syntax tooltip; other actions stay unchanged.
// Tags are inlined per test (not a shared array) — the CI tag gate (check-test-tags.js) statically
// regexes each test() call for a literal `tag: [...]` array.

test("Task Runner - select account, select Data Filter task, verify List sits above Condition with an Examples row under both and a Syntax help icon on Condition", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "data.filter", "filter");

  await expect(locators.fieldGuidance("list")).toContainText("Examples:", { timeout: 15000 });
  await expect(locators.fieldGuidance("condition")).toContainText("Examples:");
  await expect(locators.fieldSyntaxHelp("condition")).toContainText("Syntax");

  const listBox = await locators.fieldLabel("List").boundingBox();
  const conditionBox = await locators.fieldLabel("Condition").boundingBox();
  expect(listBox, "List label is not rendered").not.toBeNull();
  expect(conditionBox, "Condition label is not rendered").not.toBeNull();
  expect(listBox!.y).toBeLessThan(conditionBox!.y);
  console.log("[filter] List above Condition, both carry an Examples row");
});

test("Task Runner - select account, select Data Filter task, hover the Condition Syntax icon, verify the tooltip explains quoting and nested fields", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "data.filter", "filter");

  await locators.fieldSyntaxHelp("condition").hover();
  await expect(locators.openTooltip).toContainText("Quote your strings", { timeout: 10000 });
  await expect(locators.openTooltip).toContainText("Nested fields");
  console.log("[filter] Syntax tooltip shows the condition rules");
});

test("Task Runner - select account, select Data Filter task, click a List example and a Condition example, run task, verify only the matching item is kept", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "data.filter", "filter");

  // Index 1 is the plain-string list; object items collapse in the result tree, so they cannot prove the filter ran.
  await locators.fieldExampleChip("list", 1).click();
  await expect.poll(() => readTaskField(locators, "List"), { timeout: 10000 }).toContain('"alpha"');
  expect(await readTaskField(locators, "List")).toContain('"beta"');

  await locators.fieldExampleChip("condition", 5).click();
  await expect.poll(() => readTaskField(locators, "Condition"), { timeout: 10000 }).toBe('$ = "beta"');

  await runTask(page, locators, "Task Runner Data Filter examples");
  await expectRunSucceeded(locators);

  const output = await readRunOutput(locators);
  expect(output).toContain("beta");
  expect(output).not.toContain("alpha");
  console.log("[filter] example chips filled both fields and the run kept only the match");
});

test("Task Runner - select account, select Data Filter task, click a Condition example over typed text, verify the field is replaced by the example", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "data.filter", "filter");

  await setTaskField(locators, "Condition", "typed by hand");
  await expect.poll(() => readTaskField(locators, "Condition"), { timeout: 10000 }).toBe("typed by hand");

  await locators.fieldExampleChip("condition", 1).click();
  await expect.poll(() => readTaskField(locators, "Condition"), { timeout: 10000 }).toBe("cpu > 80");
  console.log("[filter] clicking an example replaced the existing Condition text");
});

test("Task Runner - select account, select Data Filter task, enter invalid JSON in List then correct it, verify the inline JSON error appears and clears", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional", "@negative", "@validation"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "data.filter", "filter");

  await setTaskField(locators, "List", `[{"name":'23'}]`);
  await expect(locators.invalidJsonError).toBeVisible({ timeout: 10000 });

  await setTaskField(locators, "List", `[{"name":"23"}]`);
  await expect(locators.invalidJsonError).toHaveCount(0, { timeout: 10000 });
  console.log("[filter] invalid JSON raised an inline error that cleared once corrected");
});

test("Task Runner - select account, select Data Transform task, click an Input example and an Expression example, run task, verify the projected names", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "data.transform", "transform");

  await expect(locators.fieldGuidance("input")).toContainText("Examples:", { timeout: 15000 });
  await expect(locators.fieldGuidance("expression")).toContainText("Examples:");
  await expect(locators.fieldSyntaxHelp("expression")).toBeVisible();

  await locators.fieldExampleChip("input", 0).click();
  await expect.poll(() => readTaskField(locators, "Input"), { timeout: 10000 }).toContain("checkout");

  await locators.fieldExampleChip("expression", 0).click();
  await expect.poll(() => readTaskField(locators, "Expression"), { timeout: 10000 }).toBe("pods.name");

  await runTask(page, locators, "Task Runner Data Transform examples");
  await expectRunSucceeded(locators);

  const output = await readRunOutput(locators);
  expect(output).toContain("checkout");
  expect(output).toContain("payments");
  console.log("[transform] example chips filled both fields and the run projected the names");
});

test("Task Runner - select account, select Notifications Email task, verify no Examples row or Syntax help is shown", { tag: ["@dev", "@test", "@task-runner", "@automation", "@regression", "@functional"] }, async ({ page }) => {
  test.setTimeout(150000);

  const locators = await openTaskRunner(page);
  await selectRunnerAccount(page, locators);
  await selectTask(page, locators, "notifications.email", "email");

  await expect(locators.testActionHeading).toBeVisible();
  await expect(locators.runnerBox.getByText("Examples:", { exact: true })).toHaveCount(0);
  await expect(locators.runnerBox.getByText("Syntax", { exact: true })).toHaveCount(0);
  console.log("[email] unconfigured action renders no guidance row");
});
