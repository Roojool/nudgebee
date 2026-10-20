// Not for OSS
import { test, expect } from "@playwright/test";
import { NubiLocators } from "../nubiLocators";
import {
  CASE_TIMEOUT,
  CONTROL_TIMEOUT,
  LOAD_TIMEOUT,
  MOUNT_TIMEOUT,
  openInferredTab,
  readRowValue,
  remountInferredTab,
  requireInferredRows,
  requireUnkeptRow,
  restoreUnkept,
  waitForInferredLoaded,
} from "./inferredPreferencesHelper";

// Nubi > b-Cortex > Preferences > Inferred (app/src/ee/components/memory2/InferredPreferencesTab.jsx).
//
// The review queue for preferences the backend inferred from the signed-in user's chats. The tab
// has no create path — rows are written by the backend, never entered by hand — so the three cases
// that act on a row take whatever the environment holds and state that as a precondition rather
// than seeding one. Of its three writes, only Keep is exercised: Unkeep is the product's own undo
// for it, so the round trip puts the row back where it found it. Discard is never confirmed — it
// destroys an inferred preference belonging to a shared dev user and nothing in the product can
// recreate one — so the discard case asserts the dialog and its copy, then cancels.
//
// The first seven cases hold whether the queue is empty or populated, which is what the second one
// pins down explicitly.
test.describe("Nubi Inferred Preferences Tab", () => {
  test(
    "Nubi Inferred Preferences sanity - open b-Cortex, select the Preferences group, verify it lands on Inferred with the inferred-from-chats header and the Keep and Unkeep description",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);

      await expect(locators.tabHeaderTitle).toBeVisible({ timeout: MOUNT_TIMEOUT });
      // The description is the tab's whole contract — a Keep is reversible — and it interpolates
      // the tenant's assistant name, so only the brand-free half of the sentence is matched.
      await expect(locators.tabDescription).toBeVisible({ timeout: CONTROL_TIMEOUT });
    }
  );

  test(
    "Nubi Inferred Preferences sanity - open the Inferred tab, verify the queue settles into either preference rows or the No inferred preferences yet empty state and never both",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);

      // InferredPreferencesTab renders the row card and the empty state out of one ternary, so
      // exactly one of the two has to be on screen once the fetch has settled. Which one depends
      // on what the backend has inferred for the signed-in user, so this asserts the exclusivity
      // and then the contract of whichever shape it landed in.
      const rowCount = await locators.rowActionButtons.count();
      const hasEmptyState = (await locators.emptyState.count()) === 1;
      expect(rowCount > 0).toBe(!hasEmptyState);

      if (rowCount > 0) {
        // Every row renders a Discard control alongside exactly one of Keep or Unkeep, so the two
        // counts have to agree — a row offering both or neither would mean the queue lost track of
        // what has already been reviewed.
        await expect(locators.discardButtons).toHaveCount(rowCount, { timeout: CONTROL_TIMEOUT });
        // The Kept chip and the Unkeep control are both rendered off isKeptPreference, so a row
        // badged Kept must be exactly a row offering Unkeep.
        await expect(locators.keptChips).toHaveCount(await locators.unkeepButtons.count(), { timeout: CONTROL_TIMEOUT });
      } else {
        await expect(locators.emptyState).toContainText("it will appear here for review", { timeout: CONTROL_TIMEOUT });
      }
    }
  );

  test(
    "Nubi Inferred Preferences sanity - select the Preferences group, verify its sub-tab strip offers Inferred and Typed with Inferred selected and Memory's Patterns tab withdrawn",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);

      // Inferred and Typed stay two separate sub-tabs rather than one merged screen, because they
      // hold genuinely different data shapes (BCortexModal.jsx, docs/ia-consolidation-plan.md
      // PR 4 / Decision X). The strip belongs to the active group, so Memory's own sub-tabs have
      // to be gone rather than merely unselected.
      await expect(locators.inferredTab).toHaveAttribute("aria-selected", "true", { timeout: CONTROL_TIMEOUT });
      await expect(locators.typedTab).toHaveAttribute("aria-selected", "false", { timeout: CONTROL_TIMEOUT });
      await expect(locators.patternsTab).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
    }
  );

  test(
    "Nubi Inferred Preferences - switch to the Typed sibling, verify Save, Reset and the Memory scope toggle render there, switch back to Inferred, verify all three are withdrawn",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);

      // Inferred is a review queue — each row commits on its own button — so it carries none of
      // the form furniture its Typed sibling needs. Asserted against Typed in the same case rather
      // than on its own, so "absent" cannot pass merely because a selector was wrong.
      await expect(locators.typedSaveBtn).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
      await expect(locators.typedResetBtn).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
      await expect(locators.memoryScopeToggle).toHaveCount(0, { timeout: CONTROL_TIMEOUT });

      await locators.typedTab.click();
      await expect(locators.typedSaveBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(locators.typedResetBtn).toBeVisible({ timeout: CONTROL_TIMEOUT });
      await expect(locators.memoryScopeToggle).toBeVisible({ timeout: CONTROL_TIMEOUT });

      await locators.inferredTab.click();
      await expect(locators.tabHeaderTitle).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(locators.typedSaveBtn).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
      await expect(locators.typedResetBtn).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
      await expect(locators.memoryScopeToggle).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
    }
  );

  test(
    "Nubi Inferred Preferences - select Memory > Decisions, verify the account picker renders, return to the Preferences group, verify the picker is withdrawn on Inferred",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);

      // BCortexModal resolves the account picker from the active sub-tab's own `usesAccount` flag,
      // and it computes that flag for the Memory, Knowledge and Insights groups only — neither
      // Preferences sub-tab takes an accountId, so the picker must be absent on both. Proven
      // against Decisions, which does carry the flag, so "absent" cannot pass on a wrong selector.
      await expect(locators.accountSelect).toHaveCount(0, { timeout: CONTROL_TIMEOUT });

      await locators.memoryGroupTab.click();
      await locators.decisionsTab.click();
      await expect(locators.accountSelect).toBeVisible({ timeout: LOAD_TIMEOUT });

      await locators.preferencesGroupTab.click();
      await expect(locators.tabHeaderTitle).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(locators.accountSelect).toHaveCount(0, { timeout: CONTROL_TIMEOUT });
    }
  );

  test(
    "Nubi Inferred Preferences - leave the Inferred tab for Typed and come back, verify the review queue refetches and settles into the shape it started in",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);

      const rowsBefore = await locators.rowActionButtons.count();
      const emptyBefore = await locators.emptyState.count();

      await remountInferredTab(locators);

      // BCortexModal unmounts the tab body on a sub-tab switch, so coming back re-mounts the tab
      // and refetches from the backend. The queue has to come back in the shape it was in — not
      // empty, and not doubled by a refetch appending to the rows it already held.
      await expect(locators.rowActionButtons).toHaveCount(rowsBefore, { timeout: LOAD_TIMEOUT });
      await expect(locators.emptyState).toHaveCount(emptyBefore, { timeout: CONTROL_TIMEOUT });
    }
  );

  test(
    "Nubi Inferred Preferences sanity - close b-Cortex from the Inferred tab, verify the dialog is gone and the Nubi chat box takes a message again",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);
      const nubi = new NubiLocators(page);

      await locators.closeDialogBtn.click();
      await expect(locators.bCortexDialog).toHaveCount(0, { timeout: MOUNT_TIMEOUT });

      // b-Cortex is a MUI Dialog, so while it is up its container swallows every click aimed at
      // the panel behind it. Typing into the chat box is what proves the overlay actually went
      // away rather than merely turning invisible. The draft is cleared and never sent, so this
      // leaves no conversation behind.
      const draft = `nb_e2e_inferred_prefs_${Date.now()}`;
      await nubi.chatTextbox.fill(draft);
      await expect(nubi.chatTextbox).toHaveValue(draft, { timeout: CONTROL_TIMEOUT });
      await nubi.chatTextbox.fill("");
    }
  );

  test(
    "Nubi Inferred Preferences - open the Keep dialog on the first unkept preference, confirm it, leave the tab and come back, verify the preference is badged Kept and offers Unkeep after the refetch",
    { tag: ["@dev", "@regression", "@crud", "@snackbar"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);
      await requireInferredRows(locators, 1);
      await requireUnkeptRow(locators);

      const target = locators.keepButtons.first();
      const value = await readRowValue(locators, target);
      const keptBefore = await locators.unkeepButtons.count();

      try {
        await target.click();
        await expect(locators.keepDialog).toBeVisible({ timeout: MOUNT_TIMEOUT });
        await locators.keepDialogConfirmBtn.click();
        await expect(locators.keepDialog).toHaveCount(0, { timeout: MOUNT_TIMEOUT });

        await expect(locators.toastWithText("Preference kept.")).toBeVisible({ timeout: LOAD_TIMEOUT });
        // ai_memory_confirm stamps confirmed_at server-side and the tab refetches on the back of
        // it rather than patching the row locally, so one more Unkeep control is the signal that
        // the write came back — not merely that the click was received.
        await expect(locators.unkeepButtons).toHaveCount(keptBefore + 1, { timeout: LOAD_TIMEOUT });

        // The remount is the proof the Keep persisted: it unmounts the tab and refetches, so the
        // row still reading Kept afterwards can only have come from the stored row. A toast on its
        // own would not have shown that.
        await remountInferredTab(locators);
        const row = locators.rowWithValue(value);
        await expect(locators.unkeepButtonsIn(row)).toBeVisible({ timeout: LOAD_TIMEOUT });
        await expect(locators.keptChipIn(row)).toBeVisible({ timeout: CONTROL_TIMEOUT });
      } finally {
        await restoreUnkept(locators, value);
      }
    }
  );

  test(
    "Nubi Inferred Preferences - open the Keep dialog on the first unkept preference, verify it quotes that preference and explains the reliance, cancel, verify the row still offers Keep",
    { tag: ["@dev", "@regression", "@negative"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);
      await requireInferredRows(locators, 1);
      await requireUnkeptRow(locators);

      const target = locators.keepButtons.first();
      const value = await readRowValue(locators, target);
      const keptBefore = await locators.unkeepButtons.count();

      await target.click();
      await expect(locators.keepDialog).toBeVisible({ timeout: MOUNT_TIMEOUT });
      // The dialog echoes the row it was opened from, which is what proves it bound to the
      // preference under test rather than to an empty draft.
      await expect(locators.keepDialog).toContainText(value, { timeout: CONTROL_TIMEOUT });
      await expect(locators.keepDialog).toContainText("will rely on this preference and stop overwriting it from future inference", {
        timeout: CONTROL_TIMEOUT,
      });

      await locators.keepDialogCancelBtn.click();
      await expect(locators.keepDialog).toHaveCount(0, { timeout: MOUNT_TIMEOUT });

      // Cancel must leave no trace: the row keeps the Keep control it had, and the number of kept
      // rows is unchanged, so nothing was written.
      await expect(locators.keepButtonsIn(locators.rowWithValue(value))).toBeVisible({ timeout: MOUNT_TIMEOUT });
      await expect(locators.unkeepButtons).toHaveCount(keptBefore, { timeout: CONTROL_TIMEOUT });
    }
  );

  test(
    "Nubi Inferred Preferences - open the Discard dialog on the first preference, verify it warns the preference may be re-detected, cancel, verify the preference is still listed",
    { tag: ["@dev", "@regression", "@negative"] },
    async ({ page }) => {
      test.setTimeout(CASE_TIMEOUT);
      const locators = await openInferredTab(page);
      const rowsBefore = await requireInferredRows(locators, 1);

      const target = locators.discardButtons.first();
      const value = await readRowValue(locators, target);

      await target.click();
      await expect(locators.discardDialog).toBeVisible({ timeout: MOUNT_TIMEOUT });
      await expect(locators.discardDialog).toContainText(value, { timeout: CONTROL_TIMEOUT });
      await expect(locators.discardDialog).toContainText("may re-detect it later from new conversations", { timeout: CONTROL_TIMEOUT });
      // Asserted present and then deliberately left unpressed — a discard is irreversible and this
      // row belongs to a shared dev user.
      await expect(locators.discardDialogConfirmBtn).toBeVisible({ timeout: CONTROL_TIMEOUT });

      await locators.discardDialogCancelBtn.click();
      await expect(locators.discardDialog).toHaveCount(0, { timeout: MOUNT_TIMEOUT });

      await expect(locators.rowWithValue(value)).toBeVisible({ timeout: MOUNT_TIMEOUT });
      await expect(locators.rowActionButtons).toHaveCount(rowsBefore, { timeout: CONTROL_TIMEOUT });
      // The cancel must not have cost the row its own controls either, so the queue is still
      // usable rather than merely still counted.
      await waitForInferredLoaded(locators);
    }
  );
});
