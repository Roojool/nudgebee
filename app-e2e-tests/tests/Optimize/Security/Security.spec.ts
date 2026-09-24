import { test, expect } from "@playwright/test";
import { waitForGraphQLAndValidate } from "../../utils/GraphQLNetworkWatcher";
import { parkCursor } from "../optimizeModuleHelper";
import { APPS_TABLE, CIS_TABLE, DETAILS_TABLE, IMAGES_TABLE, POSTURE_TABLE, SECURITY_SUBTABS, VM_TABLE } from "./securityLocators";
import {
  countDismissalRequests,
  dismissReason,
  expectSecurityBody,
  expectSelectedSubTab,
  expectTableSettled,
  expectUnselectedSubTab,
  findingRow,
  noMatchImage,
  NO_ACCOUNTS_COPY,
  openDismissModalFromRow,
  openFindingPanel,
  openImageScanDetails,
  openSecuritySubTab,
  openSecurityTab,
  ParkedFinding,
  pickDismissDuration,
  pickOpenFinding,
  reactivateFromRow,
  readFindingKey,
  restoreParkedFindings,
  searchFindingImage,
  selectImageScanStatus,
  snoozeReturnCopy,
  submitDismissModal,
  waitForFindingsTable,
} from "./securityHelper";

// Optimize -> Security: the tenant-level findings module at /optimise#security
// (app/src/components/optimise-new/SecurityView.tsx), which spans every account in the
// tenant. Not the per-cluster Security & Tools tabs already covered under
// tests/ClusterDetails/SecurityAndTools.
//
// Everything here is read-only. The writes this module offers act on shared dev data and
// cannot be undone from the UI: raising a PR on a finding moves it through the recommendation
// lifecycle, and the row actions file a real ticket. This suite therefore never opens a write
// modal — not even to cancel out of it, since a stray click inside one is a permanent change
// to a shared tenant. That also means the module has no create or form-validation surface
// this suite can reach; written up in the PR.
// Exception: the "Image Scan dismissal" group dismisses and snoozes, and its afterEach returns every parked finding to Open.
//
// Each sub-tab has two legitimate bodies — its view, or the copy naming the account type the
// tenant has none of — so every body assertion runs through expectSecurityBody and each test
// asserts something real on whichever branch this environment is in.
const SPEC_TIMEOUT_MS = 180000;

test.beforeEach(() => {
  test.setTimeout(SPEC_TIMEOUT_MS);
});

test.describe("Optimize Security", () => {
  test(
    "Optimize Security sanity - open the Security tab with no sub-fragment, verify the sub-tab strip lists Image Scan, CIS Scan, VM Vulnerabilities and Cloud Posture and opens on Image Scan",
    { tag: ["@dev", "@oss", "@test", "@sanity", "@functional"] },
    async ({ page }) => {
      const locators = await openSecurityTab(page);

      await test.step("Every sub-tab the module declares is on the strip", async () => {
        for (const tab of [
          locators.imageScanSubTab,
          locators.cisScanSubTab,
          locators.vmVulnerabilitiesSubTab,
          locators.cloudPostureSubTab,
        ]) {
          await expect(tab).toBeVisible({ timeout: 30000 });
        }
      });

      await test.step("Image Scan is the no-fragment default and the other three are closed", async () => {
        await expectSelectedSubTab(locators.imageScanSubTab);
        for (const tab of [locators.cisScanSubTab, locators.vmVulnerabilitiesSubTab, locators.cloudPostureSubTab]) {
          await expectUnselectedSubTab(tab);
        }
      });
    }
  );

  test(
    "Optimize Security - open the Image Scan sub-tab, verify the Apps view is the selected one and the Account, Severity and Status filters render above the apps table",
    { tag: ["@dev", "@oss", "@test", "@smoke", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "imageScan");

      const populated = await expectSecurityBody(locators, locators.imageScanRoot, NO_ACCOUNTS_COPY.cluster);
      if (!populated) return;

      await test.step("Apps is the mount default of the view toggle", async () => {
        await expect(locators.imageScanAppsOption).toHaveAttribute("aria-checked", "true", { timeout: 30000 });
        await expect(locators.imageScanImagesOption).toHaveAttribute("aria-checked", "false");
      });

      await test.step("The account filter SecurityView injects sits beside the view's own filters", async () => {
        await expect(locators.securityAccountFilter).toBeVisible({ timeout: 30000 });
        await expect(locators.imageScanSeverityFilter).toBeVisible();
        await expect(locators.imageScanStatusFilter).toBeVisible();
      });

      await test.step("The apps table reaches a terminal state", async () => {
        await expectTableSettled(locators, APPS_TABLE, locators.imageScanRoot);
      });
    }
  );

  test(
    "Optimize Security - open Image Scan, switch the view toggle from Apps to Images, verify the Image search box appears and the images table replaces the apps table",
    { tag: ["@dev", "@oss", "@test", "@regression", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "imageScan");

      const populated = await expectSecurityBody(locators, locators.imageScanRoot, NO_ACCOUNTS_COPY.cluster);
      if (!populated) return;

      await waitForFindingsTable(locators, APPS_TABLE, locators.imageScanRoot);

      await test.step("The Image search belongs to the Images view, so it is absent on Apps", async () => {
        await expect(locators.imageSearch).toHaveCount(0);
      });

      await parkCursor(page);
      await locators.imageScanImagesOption.click();

      await test.step("The toggle moves and the Images view takes over", async () => {
        await expect(locators.imageScanImagesOption).toHaveAttribute("aria-checked", "true", { timeout: 30000 });
        await expect(locators.imageSearch).toBeVisible({ timeout: 30000 });
      });

      await test.step("The apps table is gone and the images table has landed", async () => {
        await expect(page.locator(`#${APPS_TABLE}`)).toHaveCount(0, { timeout: 30000 });
        await waitForFindingsTable(locators, IMAGES_TABLE, locators.imageScanRoot);
        await expect(page.locator(`#${IMAGES_TABLE}`)).toBeVisible({ timeout: 60000 });
      });
    }
  );

  test(
    "Optimize Security - open Image Scan, switch to Images, search an image reference that cannot exist, verify the search term is kept and the images table falls to its no-data state",
    { tag: ["@dev", "@oss", "@test", "@regression", "@negative", "@search"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "imageScan");

      const populated = await expectSecurityBody(locators, locators.imageScanRoot, NO_ACCOUNTS_COPY.cluster);
      if (!populated) return;

      await parkCursor(page);
      await locators.imageScanImagesOption.click();
      await expect(locators.imageSearch).toBeVisible({ timeout: 30000 });
      await waitForFindingsTable(locators, IMAGES_TABLE, locators.imageScanRoot);

      const term = noMatchImage();
      await locators.searchAndApply(locators.imageSearch, term);

      await test.step("The field keeps what was typed", async () => {
        await expect(locators.imageSearch).toHaveValue(term, { timeout: 30000 });
      });

      await test.step("Nothing matches, so the table empties to its no-data state", async () => {
        await expect(locators.emptyStateFor(IMAGES_TABLE, locators.imageScanRoot)).toBeVisible({ timeout: 60000 });
        await expect(locators.rowsFor(IMAGES_TABLE)).toHaveCount(0);
      });
    }
  );

  // Deep-linked rather than reached by clicking the strip. Clicking it is covered by the
  // navigation test below, which asserts what that click reliably moves — the selected tab
  // and the URL. Asserting the BODY follows that click is not in this suite because it
  // currently does not, reliably: the counts behind the sub-tab chips land asynchronously
  // and rebuild filterOptions, which re-runs the page's hash effect against a stale asPath
  // and resets subTab, leaving the strip on the clicked tab and the body on the previous
  // one. Filed as a product bug in the PR, not worked around with a retry here.
  test(
    "Optimize Security - deep-link to the CIS Scan sub-tab, verify the Status filter renders and the CIS rules table reaches rows or its no-data state",
    { tag: ["@dev", "@oss", "@test", "@regression", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "cisScan");

      const populated = await expectSecurityBody(locators, locators.cisRoot, NO_ACCOUNTS_COPY.cluster);
      if (!populated) return;

      await test.step("The CIS view is the one rendered, not the image-scan view", async () => {
        await expect(locators.imageScanRoot).toHaveCount(0, { timeout: 30000 });
        await expect(locators.cisStatusFilter).toBeVisible({ timeout: 30000 });
      });

      await test.step("The CIS rules table reaches a terminal state", async () => {
        await expectTableSettled(locators, CIS_TABLE, locators.cisRoot);
      });
    }
  );

  test(
    "Optimize Security - deep-link to the VM Vulnerabilities sub-tab, verify the grouping toggle and Severity filter render and the findings table reaches rows or its no-data state",
    { tag: ["@dev", "@oss", "@test", "@regression", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "vmVulnerabilities");

      const populated = await expectSecurityBody(locators, locators.vmRoot, NO_ACCOUNTS_COPY.vm);
      if (!populated) return;

      await test.step("The view's own controls render beside the injected account filter", async () => {
        await expect(locators.vmGroupingToggle).toBeVisible({ timeout: 30000 });
        await expect(locators.vmSeverityFilter).toBeVisible();
        await expect(locators.securityAccountFilter).toBeVisible();
      });

      await test.step("The ungrouped findings table reaches a terminal state", async () => {
        await expectTableSettled(locators, VM_TABLE, locators.vmRoot);
      });
    }
  );

  test(
    "Optimize Security - deep-link to the Cloud Posture sub-tab, verify the Severity filter renders and the posture rules table reaches rows or its no-data state",
    { tag: ["@dev", "@oss", "@test", "@regression", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "cloudPosture");

      const populated = await expectSecurityBody(locators, locators.postureRoot, NO_ACCOUNTS_COPY.cloud);
      if (!populated) return;

      await test.step("The view's own controls render beside the injected account filter", async () => {
        await expect(locators.postureSeverityFilter).toBeVisible({ timeout: 30000 });
        await expect(locators.securityAccountFilter).toBeVisible();
      });

      await test.step("The posture rules table reaches a terminal state", async () => {
        await expectTableSettled(locators, POSTURE_TABLE, locators.postureRoot);
      });
    }
  );

  test(
    "Optimize Security - deep-link to the Cloud Posture sub-tab, reload the page, verify the Cloud Posture sub-tab is still the open one rather than falling back to Image Scan",
    { tag: ["@dev", "@oss", "@test", "@regression", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "cloudPosture");
      await expectSecurityBody(locators, locators.postureRoot, NO_ACCOUNTS_COPY.cloud);

      await page.reload();

      await test.step("The Security tab and its Cloud Posture sub-tab both come back", async () => {
        await expect(locators.securityTab).toHaveAttribute("data-tab-selected", "true", { timeout: 60000 });
        await expectSelectedSubTab(locators.cloudPostureSubTab);
        await expectUnselectedSubTab(locators.imageScanSubTab);
      });

      await test.step("The URL still carries the sub-fragment", async () => {
        await expect(page).toHaveURL(new RegExp(`#security/${SECURITY_SUBTABS.cloudPosture.fragment}$`), { timeout: 30000 });
      });
    }
  );

  test(
    "Optimize Security - open Image Scan, click CIS Scan, click back to Image Scan, verify each click moves both the selected sub-tab and the URL fragment",
    { tag: ["@dev", "@oss", "@test", "@regression", "@functional"] },
    async ({ page }) => {
      const locators = await openSecuritySubTab(page, "imageScan");
      await expectSecurityBody(locators, locators.imageScanRoot, NO_ACCOUNTS_COPY.cluster);

      await parkCursor(page);
      await locators.cisScanSubTab.click();

      await test.step("The first click lands on CIS Scan", async () => {
        await expectSelectedSubTab(locators.cisScanSubTab);
        await expect(page).toHaveURL(new RegExp(`#security/${SECURITY_SUBTABS.cisScan.fragment}$`), { timeout: 30000 });
      });

      await parkCursor(page);
      await locators.imageScanSubTab.click();

      await test.step("The second click comes back to Image Scan, URL included", async () => {
        await expectSelectedSubTab(locators.imageScanSubTab);
        await expectUnselectedSubTab(locators.cisScanSubTab);
        await expect(page).toHaveURL(new RegExp(`#security/${SECURITY_SUBTABS.imageScan.fragment}$`), { timeout: 30000 });
      });
    }
  );

  // #35666: the rescan that used to revert a dismissal is scheduled by the collector and cannot be forced from the UI.
  test.describe("Image Scan dismissal", () => {
    const parked: ParkedFinding[] = [];

    test.afterEach(async ({ page }) => {
      await restoreParkedFindings(page, parked);
    });

    test(
      "Optimize Security - dismiss an open image-scan finding from its row menu, reload, verify it is gone from Open and listed under Dismissed",
      { tag: ["@dev", "@oss", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant to dismiss");
        const { locators, key, row } = picked!;
        console.log(`[nb-35666] dismissing ${key.cve}`);

        await openDismissModalFromRow(locators, row);
        await test.step("A permanent dismissal is the default and says it stays until reactivated", async () => {
          await expect(locators.permanentDismissCaption).toBeVisible({
            timeout: 30000,
          });
          await expect(locators.dismissConfirmButton).toHaveText("Dismiss");
        });
        parked.push(await submitDismissModal(page, locators, testInfo.title, dismissReason()));

        await test.step("The finding leaves the Open list", async () => {
          await expect(row).toHaveCount(0, { timeout: 60000 });
        });

        await page.reload();
        const reopened = await openImageScanDetails(page);
        await selectImageScanStatus(page, reopened.locators, "Dismissed", testInfo.title);
        await searchFindingImage(page, reopened.locators, key.image, testInfo.title);

        await test.step("After a reload the finding is listed under Dismissed", async () => {
          await expect(findingRow(reopened.locators, key)).toHaveCount(1, {
            timeout: 60000,
          });
        });
      }
    );

    test(
      "Optimize Security - snooze an open image-scan finding for 7 days from its row menu, reload, verify the modal named the return date and the finding is gone from Open and listed under Dismissed",
      { tag: ["@dev", "@oss", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant to snooze");
        const { locators, key, row } = picked!;
        console.log(`[nb-35666] snoozing ${key.cve}`);

        await openDismissModalFromRow(locators, row);
        await pickDismissDuration(locators, "7 days");
        await test.step("Picking 7 days turns the action into a snooze returning in 7 days", async () => {
          await expect(locators.snoozeReturnCaption).toHaveText(snoozeReturnCopy(7), { timeout: 30000 });
          await expect(locators.dismissConfirmButton).toHaveText("Snooze");
        });
        parked.push(await submitDismissModal(page, locators, testInfo.title, dismissReason()));

        await test.step("The snoozed finding leaves the Open list", async () => {
          await expect(row).toHaveCount(0, { timeout: 60000 });
        });

        await page.reload();
        const reopened = await openImageScanDetails(page);
        await searchFindingImage(page, reopened.locators, key.image, testInfo.title);
        await test.step("After a reload the snoozed finding is still not in Open", async () => {
          await expect(findingRow(reopened.locators, key)).toHaveCount(0, {
            timeout: 60000,
          });
        });

        await selectImageScanStatus(page, reopened.locators, "Dismissed", testInfo.title);
        await test.step("A snooze is stored as a dismissal, so it is listed under Dismissed", async () => {
          await expect(findingRow(reopened.locators, key)).toHaveCount(1, {
            timeout: 60000,
          });
        });
      }
    );

    test(
      "Optimize Security - open an image-scan finding's side panel, dismiss it from the panel with no reason, verify the panel closes and the finding moves from Open to Dismissed",
      { tag: ["@dev", "@oss", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant to dismiss");
        const { locators, key, row } = picked!;
        console.log(`[nb-35666] dismissing from panel ${key.cve}`);

        await openFindingPanel(locators, row, key);
        await expect(locators.panelDismissButton, "No Dismiss / Snooze in the finding panel — the run user needs write access").toBeVisible({
          timeout: 30000,
        });
        await locators.panelDismissButton.click();
        await expect(locators.dismissDialog).toBeVisible({ timeout: 30000 });
        // Reason is optional in DismissModal, so an empty one must still be accepted.
        parked.push(await submitDismissModal(page, locators, testInfo.title, ""));

        await test.step("The panel closes once the dismissal is applied", async () => {
          await expect(locators.findingPanel).toBeHidden({ timeout: 30000 });
        });

        await test.step("The finding leaves the Open list", async () => {
          await expect(row).toHaveCount(0, { timeout: 60000 });
        });

        await selectImageScanStatus(page, locators, "Dismissed", testInfo.title);
        await test.step("The finding is listed under Dismissed", async () => {
          await expect(findingRow(locators, key)).toHaveCount(1, {
            timeout: 60000,
          });
        });
      }
    );

    test(
      "Optimize Security - open the Dismiss modal on an open image-scan finding, cancel it, verify no dismissal is sent and the finding stays in Open",
      { tag: ["@dev", "@oss", "@test", "@regression", "@negative", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant");
        const { locators, row } = picked!;

        const dismissalsSent = countDismissalRequests(page);

        await openDismissModalFromRow(locators, row);
        await locators.dismissReasonInput.fill(dismissReason());
        await locators.dismissCancelButton.click();

        await test.step("The modal closes without sending a dismissal", async () => {
          await expect(locators.dismissDialog).toBeHidden({ timeout: 30000 });
          expect(dismissalsSent()).toBe(0);
        });

        await test.step("The finding is still in the Open list", async () => {
          await expect(row).toHaveCount(1, { timeout: 30000 });
        });
      }
    );

    test(
      "Optimize Security - open the Dismiss modal on an open image-scan finding, pick a snooze duration, close it with the X, verify no dismissal is sent and the finding stays in Open",
      { tag: ["@dev", "@oss", "@test", "@regression", "@negative", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant");
        const { locators, row } = picked!;
        const dismissalsSent = countDismissalRequests(page);

        await openDismissModalFromRow(locators, row);
        await pickDismissDuration(locators, "30 days");
        await locators.dismissCloseButton.click();

        await test.step("The modal closes without sending a snooze", async () => {
          await expect(locators.dismissDialog).toBeHidden({ timeout: 30000 });
          expect(dismissalsSent()).toBe(0);
        });

        await test.step("The finding is still in the Open list", async () => {
          await expect(row).toHaveCount(1, { timeout: 30000 });
        });
      }
    );

    test(
      "Optimize Security - open the Dismiss modal on an image-scan finding, pick each snooze duration then Permanent, verify the return date and button label follow the choice",
      { tag: ["@dev", "@oss", "@test", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant");
        const { locators, row } = picked!;
        const dismissalsSent = countDismissalRequests(page);

        await openDismissModalFromRow(locators, row);

        for (const days of [7, 30, 90]) {
          await pickDismissDuration(locators, `${days} days`);
          await test.step(`${days} days shows a snooze returning on the matching date`, async () => {
            await expect(locators.snoozeReturnCaption).toHaveText(snoozeReturnCopy(days), { timeout: 30000 });
            await expect(locators.dismissConfirmButton).toHaveText("Snooze");
          });
        }

        await pickDismissDuration(locators, "Permanent");
        await test.step("Permanent switches back to a plain dismissal", async () => {
          await expect(locators.permanentDismissCaption).toBeVisible({
            timeout: 30000,
          });
          await expect(locators.snoozeReturnCaption).toHaveCount(0);
          await expect(locators.dismissConfirmButton).toHaveText("Dismiss");
        });

        await locators.dismissCancelButton.click();
        await test.step("Changing the duration alone sends nothing", async () => {
          await expect(locators.dismissDialog).toBeHidden({ timeout: 30000 });
          expect(dismissalsSent()).toBe(0);
        });
      }
    );

    test(
      "Optimize Security - switch Image Scan Details to Dismissed and search an image reference that cannot exist, verify the search term is kept and the table falls to its no-data state",
      { tag: ["@dev", "@oss", "@test", "@regression", "@negative", "@search", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const { locators, populated } = await openImageScanDetails(page);
        if (!populated) return;

        await selectImageScanStatus(page, locators, "Dismissed", testInfo.title);
        const term = noMatchImage();
        await locators.searchAndApply(locators.imageSearch, term);

        await test.step("The field keeps what was typed and nothing matches", async () => {
          await expect(locators.imageSearch).toHaveValue(term, {
            timeout: 30000,
          });
          await expect(locators.emptyStateFor(DETAILS_TABLE, locators.imageScanRoot)).toBeVisible({ timeout: 60000 });
          await expect(locators.rowsFor(DETAILS_TABLE)).toHaveCount(0);
        });
      }
    );

    test(
      "Optimize Security - open the Image Scan Status filter on the Apps view and on the Details view, verify Dismissed is offered only on Details",
      { tag: ["@dev", "@oss", "@test", "@regression", "@functional", "@nb-35666"] },
      async ({ page }) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const locators = await openSecuritySubTab(page, "imageScan");
        const populated = await expectSecurityBody(locators, locators.imageScanRoot, NO_ACCOUNTS_COPY.cluster);
        if (!populated) return;

        const options = locators.openDropdownOptions;

        await locators.imageScanStatusFilter.click();
        await test.step("The Apps view only filters by Open and Archive", async () => {
          await expect(options.filter({ hasText: "Open" }).first()).toBeVisible({ timeout: 30000 });
          await expect(options.filter({ hasText: "Archive" }).first()).toBeVisible();
          await expect(options.filter({ hasText: "Dismissed" })).toHaveCount(0);
        });
        await page.keyboard.press("Escape");
        await expect(options).toHaveCount(0, { timeout: 30000 });

        await parkCursor(page);
        await locators.imageScanDetailsOption.click();
        await expect(locators.imageScanDetailsOption).toHaveAttribute("aria-checked", "true", { timeout: 30000 });

        await locators.imageScanStatusFilter.click();
        await test.step("The Details view adds Dismissed so a parked finding can be found again", async () => {
          await expect(options.filter({ hasText: "Dismissed" }).first()).toBeVisible({ timeout: 30000 });
        });
        await page.keyboard.press("Escape");
      }
    );

    test(
      "Optimize Security - take a finding from the Image Scan Dismissed list, search its image under Open, verify the same finding is not also listed as Open",
      { tag: ["@dev", "@oss", "@test", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.setTimeout(SPEC_TIMEOUT_MS);
        const { locators, populated } = await openImageScanDetails(page);
        if (!populated) return;

        await selectImageScanStatus(page, locators, "Dismissed", testInfo.title);
        const firstDismissed = locators.rowsFor(DETAILS_TABLE).first();
        const hasDismissed = await firstDismissed.isVisible();
        test.skip(!hasDismissed, "No dismissed image-scan finding on this tenant to cross-check");

        const key = await readFindingKey(locators, firstDismissed);
        console.log(`[nb-35666] cross-checking dismissed ${key.cve}`);

        await selectImageScanStatus(page, locators, "Open", testInfo.title);
        await searchFindingImage(page, locators, key.image, testInfo.title);

        await test.step("The dismissed finding has no Open twin on the same cluster, app and package", async () => {
          await expect(
            locators.rowsFor(DETAILS_TABLE).first().or(locators.emptyStateFor(DETAILS_TABLE, locators.imageScanRoot)).first()
          ).toBeVisible({ timeout: 60000 });
          await expect(findingRow(locators, key)).toHaveCount(0);
        });
      }
    );

    test(
      "Optimize Security - dismiss an open image-scan finding, switch Status to Dismissed, reactivate it from its row menu, verify it leaves Dismissed and is back in Open",
      { tag: ["@dev", "@oss", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.fixme(true, "#39216: the Details list omits status, so a dismissed row offers Dismiss / snooze instead of Reactivate");
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant to dismiss");
        const { locators, key, row } = picked!;

        await openDismissModalFromRow(locators, row);
        parked.push(await submitDismissModal(page, locators, testInfo.title, dismissReason()));
        await expect(row).toHaveCount(0, { timeout: 60000 });

        await selectImageScanStatus(page, locators, "Dismissed", testInfo.title);
        const dismissedRow = findingRow(locators, key);
        await expect(dismissedRow).toHaveCount(1, { timeout: 60000 });

        await test.step("The dismissed row offers Reactivate and no longer offers Dismiss", async () => {
          await locators.rowMenuTrigger(dismissedRow).click();
          await expect(locators.reactivateMenuItem).toBeVisible({
            timeout: 30000,
          });
          await expect(locators.dismissMenuItem).toHaveCount(0);
          await page.keyboard.press("Escape");
        });

        await reactivateFromRow(page, locators, dismissedRow, testInfo.title);
        await test.step("Reactivating removes it from Dismissed", async () => {
          await expect(dismissedRow).toHaveCount(0, { timeout: 60000 });
        });

        await selectImageScanStatus(page, locators, "Open", testInfo.title);
        await test.step("The reactivated finding is back in the Open list", async () => {
          await expect(findingRow(locators, key)).toHaveCount(1, {
            timeout: 60000,
          });
        });
      }
    );

    test(
      "Optimize Security - dismiss an open image-scan finding, open it from the Dismissed list, verify its side panel offers Reactivate and reactivating returns it to Open",
      { tag: ["@dev", "@oss", "@regression", "@functional", "@nb-35666"] },
      async ({ page }, testInfo) => {
        test.fixme(
          true,
          "#39216: the Details list omits status, so a dismissed finding's panel offers Dismiss / Snooze instead of Reactivate"
        );
        test.setTimeout(SPEC_TIMEOUT_MS);
        const picked = await pickOpenFinding(page, testInfo.title);
        test.skip(!picked, "No open image-scan finding on this tenant to dismiss");
        const { locators, key, row } = picked!;

        await openDismissModalFromRow(locators, row);
        parked.push(await submitDismissModal(page, locators, testInfo.title, dismissReason()));
        await expect(row).toHaveCount(0, { timeout: 60000 });

        await selectImageScanStatus(page, locators, "Dismissed", testInfo.title);
        const dismissedRow = findingRow(locators, key);
        await expect(dismissedRow).toHaveCount(1, { timeout: 60000 });
        await openFindingPanel(locators, dismissedRow, key);

        await test.step("The panel of a dismissed finding offers Reactivate", async () => {
          await expect(locators.panelReactivateButton).toBeVisible({
            timeout: 30000,
          });
          await expect(locators.panelDismissButton).toHaveCount(0);
        });

        await waitForGraphQLAndValidate(page, () => locators.panelReactivateButton.click(), {
          testName: testInfo.title,
          operationNames: ["UpdateRecommendationDismissal"],
          checkDataErrors: true,
          timeoutMs: 60000,
        });

        await selectImageScanStatus(page, locators, "Open", testInfo.title);
        await test.step("The reactivated finding is back in the Open list", async () => {
          await expect(findingRow(locators, key)).toHaveCount(1, {
            timeout: 60000,
          });
        });
      }
    );
  });
});
