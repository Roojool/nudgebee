// Not for OSS
import { test, expect } from "@playwright/test";
import { trackOperations } from "../egress-filter/egressFilterHelper";
import {
  BULK_OPS,
  OP_LIST,
  expectDeclarationAbsent,
  expectDeclarationListed,
  findDeclarationRow,
  remountManualDependencies,
  sweepStaleDeclarations,
} from "../kg-manual-dependencies/kgManualDependenciesHelper";
import { CSV_COLUMNS, REQUIRED_COLUMNS, UNRESOLVED_IMPORT_STATUS } from "./kgCsvImportLocators";
import {
  OP_IMPORT,
  SUITE_HEADER,
  buildCsv,
  csvLine,
  deleteImportedRows,
  draftCsvRow,
  finishImport,
  openCsvImport,
  openImportDialog,
  pasteCsv,
  sourceEndpointText,
  submitImport,
} from "./kgCsvImportHelper";

// Nubi > b-Cortex > Knowledge Graph > Settings > Manual Dependencies > Import CSV
// (app/src/components/knowledge-graph/CsvImportDialog.jsx). Every imported row
// carries a run-unique service name no real workload has, so the resolver can
// never pin it to a live node, and every row is deleted again through its own
// menu. The toolbar's Re-resolve all and Delete all act on the whole tenant and
// are never clicked; every writing case asserts both stayed at zero. The list
// paginates client-side past ten rows, so listed/absent checks walk the pager.

test.describe("Nubi KG CSV Import", () => {
  test(
    "Nubi KG CSV Import sanity - open Import CSV, verify the dialog names the four required columns, opens on Paste with an empty body and Import disabled, paste a valid row, cancel, verify no import request is sent, reopen, verify the body is empty again",
    { tag: ["@dev", "@sanity", "@functional", "@negative"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const counts = trackOperations(page, [OP_IMPORT]);
      const locators = await openCsvImport(page);

      await expect(locators.requiredColumnsNote).toBeVisible();
      for (const column of REQUIRED_COLUMNS) {
        await expect(locators.requiredColumnsNote).toContainText(column);
      }
      await expect(locators.pasteModeBtn).toBeEnabled();
      await expect(locators.uploadModeBtn).toBeEnabled();
      await expect(locators.csvTextarea).toHaveValue("");
      await expect(locators.dropZoneText).toHaveCount(0);
      await expect(locators.importBtn).toBeDisabled();
      await expect(locators.importCancelBtn).toBeEnabled();

      const row = draftCsvRow();
      await pasteCsv(locators, buildCsv([row]));
      await expect(locators.importBtn).toBeEnabled({ timeout: 10000 });

      await locators.importCancelBtn.click();
      await locators.importDialog.waitFor({ state: "detached", timeout: 15000 });
      expect(counts[OP_IMPORT]).toBe(0);

      // handleClose resets the form, so the pasted body must not survive a reopen.
      await openImportDialog(locators);
      await expect(locators.csvTextarea).toHaveValue("");
      await expect(locators.importBtn).toBeDisabled();
      await locators.importCancelBtn.click();
      await locators.importDialog.waitFor({ state: "detached", timeout: 15000 });
      await expectDeclarationAbsent(locators, row.sourceName);
    }
  );

  test(
    "Nubi KG CSV Import sanity - open Import CSV, click View full schema, verify the recommended header row lists all sixteen columns in order and the table marks four required and twelve optional, click Hide schema, verify the panel closes",
    { tag: ["@dev", "@sanity", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openCsvImport(page);

      await expect(locators.schemaToggleBtn).toHaveText("View full schema");
      await expect(locators.schemaHeaderRow).toHaveCount(0);

      await locators.schemaToggleBtn.click();
      await expect(locators.schemaToggleBtn).toHaveText("Hide schema", { timeout: 10000 });
      await expect(locators.schemaHeaderRow).toBeVisible({ timeout: 10000 });
      await expect(locators.schemaTableRows).toHaveCount(CSV_COLUMNS.length);
      for (const [index, column] of CSV_COLUMNS.entries()) {
        await expect(locators.schemaTableRows.nth(index)).toContainText(column);
      }
      // Counted per row rather than per chip: ds/Label nests spans, so a chip's
      // text would match more than one element. No word boundaries: a row's cells
      // concatenate with no separator ("source_node_typerequiredKG NodeType…"), and
      // no purpose text contains either word, so the bare substring is exact enough.
      await expect(locators.schemaTableRows.filter({ hasText: /required/ })).toHaveCount(REQUIRED_COLUMNS.length);
      await expect(locators.schemaTableRows.filter({ hasText: /optional/ })).toHaveCount(CSV_COLUMNS.length - REQUIRED_COLUMNS.length);
      for (const column of REQUIRED_COLUMNS) {
        await expect(locators.schemaTableRows.filter({ hasText: column }).first()).toContainText("required");
      }

      await locators.schemaToggleBtn.click();
      await expect(locators.schemaToggleBtn).toHaveText("View full schema", { timeout: 10000 });
      await expect(locators.schemaHeaderRow).toHaveCount(0);
      await expect(locators.schemaTable).toHaveCount(0);
    }
  );

  test(
    "Nubi KG CSV Import - open Import CSV, switch to Upload file, verify the drop zone with its 1024 KB limit replaces the textarea and Import stays disabled, switch back to Paste, verify the empty textarea returns",
    { tag: ["@dev", "@regression", "@functional"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const locators = await openCsvImport(page);

      await locators.uploadModeBtn.click();
      await expect(locators.dropZoneText).toHaveText("Drop a CSV here or click to browse", { timeout: 10000 });
      await expect(locators.maxSizeNote).toBeVisible();
      await expect(locators.csvTextarea).toHaveCount(0);
      await expect(locators.importBtn).toBeDisabled();

      await locators.pasteModeBtn.click();
      await expect(locators.csvTextarea).toBeVisible({ timeout: 10000 });
      await expect(locators.csvTextarea).toHaveValue("");
      await expect(locators.dropZoneText).toHaveCount(0);
      await expect(locators.importBtn).toBeDisabled();
    }
  );

  test(
    "Nubi KG CSV Import - paste a CSV whose header lacks source_node_type and dest_node_type, click Import, verify the missing-columns error toast names both, the dialog stays open with the body intact and no import request is sent",
    { tag: ["@dev", "@regression", "@negative", "@validation", "@snackbar"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const counts = trackOperations(page, [OP_IMPORT]);
      const locators = await openCsvImport(page);
      const row = draftCsvRow();
      const csv = ["source_name,dest_name", `${row.sourceName},${row.destName}`].join("\n");

      await pasteCsv(locators, csv);
      await expect(locators.importBtn).toBeEnabled({ timeout: 10000 });
      await locators.importBtn.click();

      // validateCsvHeader reports the missing names in REQUIRED_COLUMNS order.
      await expect(locators.errorToast("CSV header missing required columns: source_node_type, dest_node_type")).toBeVisible({ timeout: 15000 });
      await expect(locators.importDialog).toBeVisible();
      await expect(locators.resultSummary).toHaveCount(0);
      await expect(locators.csvTextarea).toHaveValue(csv);
      expect(counts[OP_IMPORT]).toBe(0);

      await locators.importCancelBtn.click();
      await locators.importDialog.waitFor({ state: "detached", timeout: 15000 });
      await expectDeclarationAbsent(locators, row.sourceName);
    }
  );

  test(
    "Nubi KG CSV Import - paste a body of only whitespace, verify Import enables, click it, verify the empty-body error toast, the dialog stays open and no import request is sent",
    { tag: ["@dev", "@regression", "@negative", "@validation", "@snackbar"] },
    async ({ page }) => {
      test.setTimeout(240000);
      const counts = trackOperations(page, [OP_IMPORT]);
      const locators = await openCsvImport(page);

      // The button gates on a non-empty string, so whitespace passes it and the
      // trim check inside handleImport is what has to reject the body.
      await pasteCsv(locators, "   \n  \n");
      await expect(locators.importBtn).toBeEnabled({ timeout: 10000 });
      await locators.importBtn.click();

      await expect(locators.errorToast("CSV body is empty.")).toBeVisible({ timeout: 15000 });
      await expect(locators.importDialog).toBeVisible();
      await expect(locators.resultSummary).toHaveCount(0);
      expect(counts[OP_IMPORT]).toBe(0);
    }
  );

  test(
    "Nubi KG CSV Import - paste one PUBLISHES_TO row with namespace and cluster, click Import, verify 1 imported and 0 rejected with row 0 unresolved, click Done, verify the list reloads and shows the row with its qualifiers and PUBLISHES_TO, close and reopen Settings, verify it persisted",
    { tag: ["@dev", "@smoke", "@crud"] },
    async ({ page }) => {
      test.setTimeout(300000);
      const counts = trackOperations(page, [...BULK_OPS, OP_IMPORT]);
      const locators = await openCsvImport(page);
      const row = draftCsvRow("PUBLISHES_TO");
      try {
        await pasteCsv(locators, buildCsv([row]));
        await submitImport(page, locators, { imported: 1, rejected: 0 });
        await expect(locators.importedHeading).toBeVisible();
        await expect(locators.rejectedHeading).toHaveCount(0);
        await expect(locators.importedRowLine(0)).toBeVisible();
        await expect(locators.importedRowStatus(0)).toHaveText(UNRESOLVED_IMPORT_STATUS);
        await expect(locators.importBtn).toHaveCount(0);

        await finishImport(page, locators);
        await expectDeclarationListed(locators, row.sourceName);
        await expect(locators.declarationRow(row.sourceName)).toContainText(sourceEndpointText(row));
        await expect(locators.declarationRow(row.sourceName)).toContainText(row.destName);
        await expect(locators.rowRelationshipLabel(row.sourceName)).toHaveText("PUBLISHES_TO");
        await expect(locators.rowStatusLabel(row.sourceName)).not.toHaveText("Resolved");

        await remountManualDependencies(locators);
        await expectDeclarationListed(locators, row.sourceName);
        await expect(locators.rowRelationshipLabel(row.sourceName)).toHaveText("PUBLISHES_TO");
        expect(counts[OP_IMPORT]).toBe(1);
        for (const op of BULK_OPS) expect(counts[op], `${op} must never fire from this suite`).toBe(0);
      } finally {
        await deleteImportedRows(page, locators, [row.sourceName]);
      }
    }
  );

  test(
    "Nubi KG CSV Import - paste two rows with a blank relationship_type, click Import, verify 2 imported and 0 rejected numbered row 0 and row 1, click Done, verify both rows are listed with the CALLS default",
    { tag: ["@dev", "@regression", "@crud"] },
    async ({ page }) => {
      test.setTimeout(300000);
      const counts = trackOperations(page, [...BULK_OPS, OP_IMPORT]);
      const locators = await openCsvImport(page);
      // Leftovers of an earlier failed run share this prefix and would only add
      // pages to walk; the sweep keeps the tenant tidy before more rows go in.
      await locators.importCancelBtn.click();
      await locators.importDialog.waitFor({ state: "detached", timeout: 15000 });
      await sweepStaleDeclarations(page, locators);
      await openImportDialog(locators);

      const first = draftCsvRow();
      const second = draftCsvRow();
      try {
        await pasteCsv(locators, buildCsv([first, second]));
        await submitImport(page, locators, { imported: 2, rejected: 0 });
        await expect(locators.importedRowLine(0)).toBeVisible();
        await expect(locators.importedRowLine(1)).toBeVisible();
        await expect(locators.importedRowLine(2)).toHaveCount(0);
        await expect(locators.rejectedHeading).toHaveCount(0);

        await finishImport(page, locators);
        for (const row of [first, second]) {
          await expectDeclarationListed(locators, row.sourceName);
          await expect(locators.declarationRow(row.sourceName)).toContainText(sourceEndpointText(row));
          await expect(locators.rowRelationshipLabel(row.sourceName)).toHaveText("CALLS");
        }
        expect(counts[OP_IMPORT]).toBe(1);
        for (const op of BULK_OPS) expect(counts[op], `${op} must never fire from this suite`).toBe(0);
      } finally {
        await deleteImportedRows(page, locators, [first.sourceName, second.sourceName]);
      }
    }
  );

  test(
    "Nubi KG CSV Import - paste a valid row and a row whose relationship_type is BOGUS, click Import, verify 1 imported and 1 rejected with row 1 carrying the unsupported relationship error, click Done, verify only the valid row is listed",
    { tag: ["@dev", "@regression", "@negative", "@crud"] },
    async ({ page }) => {
      test.setTimeout(300000);
      const counts = trackOperations(page, [...BULK_OPS, OP_IMPORT]);
      const locators = await openCsvImport(page);
      const valid = draftCsvRow("CALLS");
      const bogus = draftCsvRow("BOGUS");
      try {
        await pasteCsv(locators, buildCsv([valid, bogus]));
        await submitImport(page, locators, { imported: 1, rejected: 1 });
        await expect(locators.importedHeading).toBeVisible();
        await expect(locators.importedRowLine(0)).toBeVisible();
        await expect(locators.rejectedHeading).toBeVisible();
        // ValidateRelationshipType on the backend names the offending value.
        await expect(locators.rejectedRowBox(1)).toContainText("unsupported relationship_type");
        await expect(locators.rejectedRowBox(1)).toContainText("BOGUS");

        await finishImport(page, locators);
        await expectDeclarationListed(locators, valid.sourceName);
        await expect(locators.rowRelationshipLabel(valid.sourceName)).toHaveText("CALLS");
        await expectDeclarationAbsent(locators, bogus.sourceName);
        expect(counts[OP_IMPORT]).toBe(1);
        for (const op of BULK_OPS) expect(counts[op], `${op} must never fire from this suite`).toBe(0);
      } finally {
        await deleteImportedRows(page, locators, [valid.sourceName, bogus.sourceName]);
      }
    }
  );

  test(
    "Nubi KG CSV Import - paste a valid row and a row whose dest_name is blank, click Import, verify 1 imported and 1 rejected with row 1 reporting dest_name is required, click Done, verify only the valid row is listed",
    { tag: ["@dev", "@regression", "@negative", "@validation", "@crud"] },
    async ({ page }) => {
      test.setTimeout(300000);
      const counts = trackOperations(page, [...BULK_OPS, OP_IMPORT]);
      const locators = await openCsvImport(page);
      const valid = draftCsvRow("CALLS");
      const blankDest = draftCsvRow("CALLS");
      blankDest.destName = "";
      try {
        await pasteCsv(locators, [SUITE_HEADER, csvLine(valid), csvLine(blankDest)].join("\n"));
        await submitImport(page, locators, { imported: 1, rejected: 1 });
        await expect(locators.importedRowLine(0)).toBeVisible();
        // The header carries dest_name, so the client-side header check passes and
        // the backend's ValidateEndpoint is what rejects the empty value.
        await expect(locators.rejectedRowBox(1)).toContainText("dest_name is required");

        await finishImport(page, locators);
        await expectDeclarationListed(locators, valid.sourceName);
        await expectDeclarationAbsent(locators, blankDest.sourceName);
        expect(counts[OP_IMPORT]).toBe(1);
        for (const op of BULK_OPS) expect(counts[op], `${op} must never fire from this suite`).toBe(0);
      } finally {
        await deleteImportedRows(page, locators, [valid.sourceName, blankDest.sourceName]);
      }
    }
  );

  test(
    "Nubi KG CSV Import - switch to Upload file, choose a one-row CSV file, verify the drop zone names the file and Import enables, click Import, verify 1 imported and 0 rejected, click Done, verify the row is listed with its qualifiers",
    { tag: ["@dev", "@regression", "@functional", "@crud"] },
    async ({ page }) => {
      test.setTimeout(300000);
      const counts = trackOperations(page, [...BULK_OPS, OP_IMPORT]);
      const locators = await openCsvImport(page);
      const row = draftCsvRow("SUBSCRIBES_TO");
      const fileName = `${row.sourceName}.csv`;
      try {
        await locators.uploadModeBtn.click();
        await expect(locators.dropZoneText).toHaveText("Drop a CSV here or click to browse", { timeout: 10000 });
        await expect(locators.importBtn).toBeDisabled();

        await locators.csvFileInput.setInputFiles({ name: fileName, mimeType: "text/csv", buffer: Buffer.from(buildCsv([row]), "utf8") });
        await expect(locators.dropZoneText).toHaveText(`Selected: ${fileName}`, { timeout: 15000 });
        await expect(locators.importBtn).toBeEnabled({ timeout: 10000 });

        await submitImport(page, locators, { imported: 1, rejected: 0 });
        await expect(locators.importedRowLine(0)).toBeVisible();

        await finishImport(page, locators);
        await expectDeclarationListed(locators, row.sourceName);
        await expect(locators.declarationRow(row.sourceName)).toContainText(sourceEndpointText(row));
        await expect(locators.rowRelationshipLabel(row.sourceName)).toHaveText("SUBSCRIBES_TO");
        expect(counts[OP_IMPORT]).toBe(1);
        for (const op of BULK_OPS) expect(counts[op], `${op} must never fire from this suite`).toBe(0);
      } finally {
        await deleteImportedRows(page, locators, [row.sourceName]);
      }
    }
  );

  test(
    "Nubi KG CSV Import - import one row, click Done, verify the list read fires once, open Import CSV again, verify it opens on an empty Paste view, cancel, verify the imported row is still listed and no second import request was sent",
    { tag: ["@dev", "@regression", "@functional", "@crud"] },
    async ({ page }) => {
      test.setTimeout(300000);
      const counts = trackOperations(page, [...BULK_OPS, OP_IMPORT, OP_LIST]);
      const locators = await openCsvImport(page);
      const row = draftCsvRow("CALLS");
      try {
        await pasteCsv(locators, buildCsv([row]));
        await submitImport(page, locators, { imported: 1, rejected: 0 });
        const listReadsBeforeDone = counts[OP_LIST];
        await finishImport(page, locators);
        expect(counts[OP_LIST]).toBe(listReadsBeforeDone + 1);
        expect(await findDeclarationRow(locators, row.sourceName)).toBe(true);

        // handleDone resets the dialog, so the next open must not show the old
        // body or the previous result.
        await openImportDialog(locators);
        await expect(locators.csvTextarea).toHaveValue("");
        await expect(locators.resultSummary).toHaveCount(0);
        await expect(locators.importBtn).toBeDisabled();
        await locators.importCancelBtn.click();
        await locators.importDialog.waitFor({ state: "detached", timeout: 15000 });

        await expectDeclarationListed(locators, row.sourceName);
        expect(counts[OP_IMPORT]).toBe(1);
        for (const op of BULK_OPS) expect(counts[op], `${op} must never fire from this suite`).toBe(0);
      } finally {
        await deleteImportedRows(page, locators, [row.sourceName]);
      }
    }
  );
});
