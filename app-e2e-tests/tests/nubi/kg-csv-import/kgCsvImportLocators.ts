// Not for OSS
import { Page, Locator } from "@playwright/test";
import { KgManualDependenciesLocators } from "../kg-manual-dependencies/kgManualDependenciesLocators";

// Nubi > b-Cortex > Knowledge Graph > Settings > Manual Dependencies > Import CSV
// (app/src/components/knowledge-graph/CsvImportDialog.jsx). `grep -c data-testid`
// returns 0 for the dialog, so nothing here is reachable at rung 1. Its buttons
// carry stable visible labels and are rung 2; the textarea is a bare <textarea>
// with a placeholder and no label, so it is rung 4 scoped to the dialog; the
// result and schema views are plain text and are rung 4 scoped the same way. The
// journey up to the dialog reuses the Manual Dependencies locators wholesale.

// CSV_SCHEMA in CsvImportDialog.jsx, in its render order; the schema panel prints
// this exact header row and one table row per column.
export const CSV_COLUMNS = [
  "source_node_type",
  "source_name",
  "source_namespace",
  "source_cluster",
  "source_arn",
  "source_account_id",
  "source_region",
  "dest_node_type",
  "dest_name",
  "dest_namespace",
  "dest_cluster",
  "dest_arn",
  "dest_account_id",
  "dest_region",
  "relationship_type",
  "notes",
] as const;
export const CSV_HEADER_ROW = CSV_COLUMNS.join(",");
export const REQUIRED_COLUMNS = ["source_node_type", "source_name", "dest_node_type", "dest_name"] as const;

// The result view prints each imported row's resolution status with underscores
// replaced by spaces. An invented service name can never resolve, so "resolved"
// is deliberately absent from what a row of this suite may show.
export const UNRESOLVED_IMPORT_STATUS =
  /^(pending|source unmatched|dest unmatched|source ambiguous|dest ambiguous|source too many matches|dest too many matches|node inactive|invalid payload|imported)$/;

function exactText(label: string): RegExp {
  return new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`);
}

export class KgCsvImportLocators extends KgManualDependenciesLocators {
  readonly importDialog: Locator;
  readonly importDialogCloseBtn: Locator;
  readonly requiredColumnsNote: Locator;
  readonly schemaToggleBtn: Locator;
  readonly schemaHeaderRow: Locator;
  readonly schemaTable: Locator;
  readonly schemaTableRows: Locator;
  readonly pasteModeBtn: Locator;
  readonly uploadModeBtn: Locator;
  readonly csvTextarea: Locator;
  readonly dropZoneText: Locator;
  readonly maxSizeNote: Locator;
  readonly csvFileInput: Locator;
  readonly importCancelBtn: Locator;
  readonly importBtn: Locator;
  readonly resultSummary: Locator;
  readonly importedHeading: Locator;
  readonly rejectedHeading: Locator;
  readonly doneBtn: Locator;

  constructor(page: Page) {
    super(page);

    // ds/Modal portals every dialog to <body> and stamps #alert-dialog-title on each,
    // so the import dialog is matched on its own title element, by attribute rather
    // than getByRole("dialog"): while it is open MUI marks the two dialogs under it
    // aria-hidden, and a role lookup would then misreport which one is on screen.
    this.importDialog = page
      .locator('[role="dialog"]')
      .filter({ has: page.locator("#alert-dialog-title").filter({ hasText: /^Import Manual Dependencies \(CSV\)$/ }) })
      .first();
    this.importDialogCloseBtn = this.importDialog
      .getByRole("button", { name: "Close", exact: true })
      .or(this.importDialog.locator("#close-modal-btn"))
      .first();

    // The guidance paragraph names the four required columns in <strong>; text is
    // the only handle it has.
    this.requiredColumnsNote = this.importDialog.getByText(/^Required columns:/).first();

    // One button that reads "View full schema" while the panel is closed and "Hide
    // schema" while it is open, so its name doubles as the panel's state.
    this.schemaToggleBtn = this.importDialog
      .getByRole("button", { name: /^(View full schema|Hide schema)$/ })
      .or(this.importDialog.locator("button").filter({ hasText: /^(View full schema|Hide schema)$/ }))
      .first();
    // The recommended header row is printed verbatim in a <pre>, which is the only
    // element in the dialog that holds all sixteen names in one text node.
    this.schemaHeaderRow = this.importDialog
      .getByText(CSV_HEADER_ROW, { exact: true })
      .or(this.importDialog.locator("pre").filter({ hasText: CSV_HEADER_ROW }))
      .first();
    this.schemaTable = this.importDialog.getByRole("table").or(this.importDialog.locator("table")).first();
    // Body rows only: the header row is a <thead> row and would inflate the count.
    this.schemaTableRows = this.schemaTable.locator("tbody").getByRole("row").or(this.schemaTable.locator("tbody tr"));

    this.pasteModeBtn = this.modeBtn("Paste");
    this.uploadModeBtn = this.modeBtn("Upload file");

    // A bare MUI Box rendered as <textarea> with a placeholder and no label; the
    // dialog holds exactly one textbox, which is what makes the role fallback safe.
    this.csvTextarea = this.importDialog.getByPlaceholder(/^Paste CSV body here/).or(this.importDialog.getByRole("textbox")).first();

    // The drop zone is a plain clickable Box with no role or id, and its caption
    // reads either the idle prompt or "Selected: <file>" once a file is picked;
    // scoped text is the highest rung there is, and both states are one text node.
    this.dropZoneText = this.importDialog.getByText(/^(Drop a CSV here or click to browse|Selected: .+)$/).first();
    this.maxSizeNote = this.importDialog.getByText("Max 1024 KB", { exact: true }).first();
    // The dialog's only file input; hidden by style, so it has no role or name and a
    // type match is the one honest handle. Playwright fills hidden inputs directly.
    this.csvFileInput = this.importDialog.locator('input[type="file"]');

    // "Import" exact, so the settings toolbar's "Import CSV" underneath can never
    // satisfy it even before the dialog scope is applied.
    this.importCancelBtn = this.importDialog
      .getByRole("button", { name: "Cancel", exact: true })
      .or(this.importDialog.locator("button").filter({ hasText: /^Cancel$/ }))
      .first();
    this.importBtn = this.importDialog
      .getByRole("button", { name: "Import", exact: true })
      .or(this.importDialog.locator("button").filter({ hasText: /^Import$/ }))
      .first();

    // ResultView: one summary line, an "Imported" and a "Rejected" section rendered
    // only when non-empty, and a Done button. The lines are Typography with no role
    // or id, so scoped exact text is the highest rung, and each string appears once.
    this.resultSummary = this.importDialog.getByText(/^\d+ imported, \d+ rejected$/).first();
    this.importedHeading = this.importDialog.getByText("Imported", { exact: true }).first();
    this.rejectedHeading = this.importDialog.getByText("Rejected", { exact: true }).first();
    this.doneBtn = this.importDialog
      .getByRole("button", { name: "Done", exact: true })
      .or(this.importDialog.locator("button").filter({ hasText: /^Done$/ }))
      .first();
  }

  // The Paste / Upload file pair are plain ds/Buttons whose tone flips with the
  // mode; nothing on them exposes the selected state, so mode is asserted through
  // which body (textarea or drop zone) is on screen.
  private modeBtn(label: string): Locator {
    return this.importDialog
      .getByRole("button", { name: label, exact: true })
      .or(this.importDialog.locator("button").filter({ hasText: exactText(label) }))
      .first();
  }

  // One imported row's line, "Row #<n> → id <id>"; the backend numbers data rows
  // from 0.
  importedRowLine(rowIndex: number): Locator {
    return this.importDialog.getByText(new RegExp(`^Row #${rowIndex} → id \\d+$`)).first();
  }

  // The status chip beside that line. ds/Label renders as nested spans with no
  // role, so the chip is an exact-text match inside the row's own flex box.
  importedRowStatus(rowIndex: number): Locator {
    return this.importedRowLine(rowIndex).locator("xpath=..").getByText(UNRESOLVED_IMPORT_STATUS).first();
  }

  // One rejected row's box, which holds "Row #<n>" and the backend's error text.
  rejectedRowBox(rowIndex: number): Locator {
    return this.importDialog.getByText(`Row #${rowIndex}`, { exact: true }).locator("xpath=..");
  }

  // SnackbarComponent renders an error toast as role="alert" inside a
  // region labelled "Notifications". Matched by attribute rather than getByRole:
  // the toast mounts in the app root, which MUI aria-hides while a dialog is open,
  // and every toast this suite reads fires with the import dialog open.
  errorToast(text: string | RegExp): Locator {
    return this.page
      .locator('[aria-label="Notifications"] [role="alert"]')
      .filter({ hasText: text })
      .or(this.page.locator('[aria-label="Notifications"]').getByText(text))
      .first();
  }

  // The success toast is role="status" in the same region.
  successToast(text: string | RegExp): Locator {
    return this.page
      .locator('[aria-label="Notifications"] [role="status"]')
      .filter({ hasText: text })
      .or(this.page.locator('[aria-label="Notifications"]').getByText(text))
      .first();
  }
}
