// Not for OSS
import { Page, expect } from "@playwright/test";
import { LoginPage } from "../../../pages/LoginPage";
import { NubiLocators } from "../nubiLocators";
import { waitForGraphQLAndValidate } from "../../utils/GraphQLNetworkWatcher";
import {
  OP_LIST,
  deleteDeclarationIfPresent,
  openSettingsOnManualDependencies,
  uniqueDeclarationName,
  waitForListLoaded,
} from "../kg-manual-dependencies/kgManualDependenciesHelper";
import { KgCsvImportLocators } from "./kgCsvImportLocators";

// app/src/api1/knowledge-graph/index.ts names the CSV import call this way.
export const OP_IMPORT = "KgImportManualDependencies";

// The header this suite pastes: the four required columns plus the qualifiers and
// relationship the table renders back, so an imported row can be recognised by
// its Source → Destination cell and its relationship chip.
export const SUITE_HEADER = "source_node_type,source_name,source_namespace,source_cluster,dest_node_type,dest_name,relationship_type,notes";

export interface CsvRowDraft {
  sourceName: string;
  sourceNamespace: string;
  sourceCluster: string;
  destName: string;
  relationship: string;
  notes: string;
}

// Names come from the sibling suite's generator so they carry its shared prefix:
// its stale-row sweep then covers anything a failed run of this suite leaves
// behind. Namespace and cluster are invented qualifiers that only have to round-
// trip into the row's Source cell.
export function draftCsvRow(relationship = ""): CsvRowDraft {
  return {
    sourceName: uniqueDeclarationName("src"),
    sourceNamespace: "nb-e2e-ns",
    sourceCluster: "nb-e2e-cluster",
    destName: uniqueDeclarationName("dst"),
    relationship,
    notes: "declared by the Nubi KG CSV import e2e",
  };
}

export function csvLine(row: CsvRowDraft): string {
  return ["K8sService", row.sourceName, row.sourceNamespace, row.sourceCluster, "K8sService", row.destName, row.relationship, row.notes].join(",");
}

export function buildCsv(rows: CsvRowDraft[], header = SUITE_HEADER): string {
  return [header, ...rows.map(csvLine)].join("\n");
}

// The Source → Destination cell as ManualDependenciesTable formats a Kubernetes
// endpoint with both qualifiers set.
export function sourceEndpointText(row: CsvRowDraft): string {
  return `K8sService: ${row.sourceNamespace}/${row.sourceName} @ ${row.sourceCluster}`;
}

// Logs in, lands on b-Cortex > Knowledge Graph > Settings > Manual Dependencies
// and opens the Import CSV dialog on its empty Paste view.
export async function openCsvImport(page: Page): Promise<KgCsvImportLocators> {
  const loginPage = new LoginPage(page);
  const nubi = new NubiLocators(page);
  const locators = new KgCsvImportLocators(page);

  await loginPage.doFullLogin();
  await nubi.openPanel();
  await nubi.openBCortex(locators.knowledgeGraphTab);
  await locators.knowledgeGraphTab.click();
  await expect(locators.kgCanvas).toBeVisible({ timeout: 60000 });
  // Settings renders only for a tenant admin, so its absence is a precondition
  // failure worth naming rather than a locator timeout further down the case.
  await expect(locators.kgSettingsBtn, "the Knowledge Graph Settings control is missing — it renders only for a tenant admin").toBeVisible({
    timeout: 30000,
  });
  await openSettingsOnManualDependencies(locators);
  await openImportDialog(locators);
  return locators;
}

export async function openImportDialog(locators: KgCsvImportLocators): Promise<void> {
  await locators.importCsvBtn.click();
  await expect(locators.importDialog).toBeVisible({ timeout: 15000 });
  await expect(locators.csvTextarea).toBeVisible({ timeout: 15000 });
}

export async function pasteCsv(locators: KgCsvImportLocators, csv: string): Promise<void> {
  await locators.csvTextarea.fill(csv);
  await expect(locators.csvTextarea).toHaveValue(csv, { timeout: 10000 });
}

// Submits the pasted body, asserting the import mutation left the browser and came
// back clean, then reads the result summary the dialog swaps in.
export async function submitImport(page: Page, locators: KgCsvImportLocators, expected: { imported: number; rejected: number }): Promise<void> {
  await expect(locators.importBtn).toBeEnabled({ timeout: 10000 });
  await waitForGraphQLAndValidate(
    page,
    async () => {
      await locators.importBtn.click();
      await expect(locators.resultSummary).toBeVisible({ timeout: 30000 });
    },
    { testName: "KG CSV import - import a CSV body", operationNames: OP_IMPORT, timeoutMs: 45000 }
  );
  await expect(locators.resultSummary).toHaveText(`${expected.imported} imported, ${expected.rejected} rejected`);
}

// Done closes the dialog and the tab refetches its list; the read is asserted so
// the listed/absent checks that follow cannot run against the pre-import table.
export async function finishImport(page: Page, locators: KgCsvImportLocators): Promise<void> {
  await waitForGraphQLAndValidate(
    page,
    async () => {
      await locators.doneBtn.click();
      await locators.importDialog.waitFor({ state: "detached", timeout: 30000 });
    },
    { testName: "KG CSV import - close the result view", operationNames: OP_LIST, timeoutMs: 45000 }
  );
  await waitForListLoaded(locators);
}

// Teardown for rows this suite imported. A failed step may have left the import
// dialog on top, in either of its views; its close icon resets and closes both,
// after which the sibling teardown reopens Settings and deletes each row through
// its own menu, asserting the delete landed.
export async function deleteImportedRows(page: Page, locators: KgCsvImportLocators, sourceNames: string[]): Promise<void> {
  try {
    // A test that never opened, or already closed, the dialog is the normal case
    // here, so absence has to come back as false rather than throw.
    if (await locators.importDialog.isVisible().catch(() => false)) {
      await locators.importDialogCloseBtn.click();
      await locators.importDialog.waitFor({ state: "detached", timeout: 15000 });
    }
  } catch (error) {
    console.warn(`[cleanup] the import dialog could not be closed: ${error}`);
  }
  for (const name of sourceNames) {
    await deleteDeclarationIfPresent(page, locators, name);
  }
}
