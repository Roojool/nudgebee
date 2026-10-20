// Not for OSS
import { Page, Locator, expect } from "@playwright/test";
import { AWSLocators } from "../AWSLocators";
// escapeForRegex and stateOptionLabel are provider-agnostic string helpers that already
// exist in the EC2 module. Imported rather than mirrored so there is one copy to keep
// correct; promoting them to a shared AWS helper is raised under Follow-ups.
import { escapeForRegex } from "../EC2/ec2Constants";
import {
  DRILLDOWN_TABS,
  FILTER,
  FILTER_LABEL,
  INSTANCES_TABLE,
  renderedColumnLabel,
  SEARCH_INPUT_ID,
  SEARCH_PLACEHOLDER,
  STATE_COLUMN,
} from "./rdsConstants";

// Nothing on this surface carries a data-testid — `grep -c data-testid` returns 0 for
// every file under app/src/components/cloudaccount/rds/ — so rung 1 of the locator
// ladder does not exist here and the ids the components are explicitly given are the
// primary, with a role-based fallback scoped to the same container wherever the
// accessible name is unambiguous. The one exception is ConfirmActionDialog, which does
// render testids; that one is used as the primary below.
//
// Every instance row emits a second <tr> holding the collapsed drill-down, present in
// the DOM whether or not the row is open and carrying a single colSpan cell. Requiring
// a second cell is what separates the data rows from it — counting `tr` alone would
// silently double every row count in this file.
const DATA_ROW = "tr:has(td:nth-child(2))";

// Extends AWSLocators rather than CommonLocators directly: openAWSCloudAccountFromConfig
// (the integration gate plus the account picker) and navigateToSubTab already live
// there and are what every test here starts from.
export class RdsLocators extends AWSLocators {
  readonly instancesRoot: Locator;
  readonly instancesTable: Locator;
  readonly instancesBody: Locator;
  readonly instancesEmpty: Locator;
  readonly instancesRows: Locator;
  readonly instancesSearch: Locator;
  readonly refreshButton: Locator;
  readonly downloadButton: Locator;

  readonly statTotalInstances: Locator;
  readonly statFiredAlarmCount: Locator;
  readonly statOptimizeCount: Locator;
  readonly metricsCard: Locator;

  readonly confirmDialog: Locator;
  readonly allDialogs: Locator;
  readonly confirmDialogTitle: Locator;
  readonly confirmDialogCancel: Locator;
  readonly confirmDialogSubmit: Locator;
  readonly strictConfirmInput: Locator;

  constructor(page: Page) {
    super(page);

    // InstancesView hardcodes id='right-sizing' on its ListingLayout (Instances.tsx:908),
    // which puts it on the wrapping DS Card. Deliberately id-only: the Card is a plain
    // styled div with no role and no accessible name, and it is the container the
    // toolbar assertions scope to — a wider fallback here would silently widen those
    // too. The id is a poor name for this listing and is shared with the EC2/S3/ECS
    // instance views; only one of them is ever mounted, so it still resolves uniquely.
    this.instancesRoot = page.locator("#right-sizing").first();

    // CustomTable renders id={id} on the <table>, whose only accessible name is
    // aria-label='table' (CustomTable.jsx:1120).
    this.instancesTable = page.locator(`#${INSTANCES_TABLE}`).or(this.instancesRoot.getByRole("table")).first();
    // Body and rows are deliberately id-only: the id'd <tbody> (CustomTable.jsx:868)
    // replacing the skeleton one is the settle signal waitForInstances relies on, and
    // any wider match would resolve the skeleton too and report the table ready before
    // its rows exist.
    this.instancesBody = page.locator(`#${INSTANCES_TABLE}-body`);
    this.instancesRows = page.locator(`#${INSTANCES_TABLE}-body ${DATA_ROW}`);

    // EmptyData renders <h2 id={`${id}-no-data`}> (EmptyData.jsx:26). Scoped heading
    // fallback rather than a bare getByText: "No Data Available" is CustomTable's
    // default empty copy and appears on every table in the app.
    this.instancesEmpty = page
      .locator(`#${INSTANCES_TABLE}-no-data`)
      .or(this.instancesRoot.getByRole("heading", { name: "No Data Available" }))
      .first();

    // ds/SearchInput puts its id on the <input> itself and renders `label` as the
    // placeholder, so the placeholder is the accessible name. RDS searches by instance
    // name only — EC2's box also matches the instance id.
    this.instancesSearch = page
      .locator(`#${SEARCH_INPUT_ID}`)
      .or(this.instancesRoot.getByPlaceholder(SEARCH_PLACEHOLDER))
      .first();

    // ServiceRefreshButton builds `service-refresh-${id}` and sets aria-label='Refresh'
    // (ServiceRefreshButton.tsx:58, 63). DownloadButton takes its id verbatim and sets
    // aria-label='Download'.
    this.refreshButton = page
      .locator(`#service-refresh-${INSTANCES_TABLE}`)
      .or(this.instancesRoot.getByRole("button", { name: "Refresh" }))
      .first();
    this.downloadButton = page
      .locator(`#${INSTANCES_TABLE}-download`)
      .or(this.instancesRoot.getByRole("button", { name: "Download" }))
      .first();

    // ds/Stat puts `id` on its root Box (the ids come from Summary.tsx:318, 421, 429).
    // No fallback: a Stat is a plain Box with no role, and its label text also appears
    // in the tile's own body, so any text-based widening would match two nodes.
    this.statTotalInstances = page.locator("#rds-summary-total-instances");
    this.statFiredAlarmCount = page.locator("#rds-summary-fired-alarm-count");
    this.statOptimizeCount = page.locator("#rds-summary-optimize-count");
    // ListingLayout puts `id` on the wrapping DS Card (Summary.tsx:523), a plain styled
    // div with no role. Id-only for the same reason as instancesRoot: a heading-based
    // fallback would resolve the "Metrics" toolbar title, not the card that contains it.
    this.metricsCard = page.locator("#rds-metrics");

    // ConfirmActionDialog renders through ds/Modal, which is a MUI Dialog carrying
    // id='alert-dialog-title' on its heading and id='cancel' / id='submit' on the
    // standard footer buttons (Modal.tsx:357, 476, 481).
    //
    // Unfiltered on purpose, for the "it closed" assertion. MUI Dialog unmounts on
    // close, so the check there is toHaveCount(0) rather than toBeHidden — but taken
    // through confirmDialog it would cap at one and could never see a second dialog
    // left open behind the first, which is the regression the strict form exists for.
    //
    // The dialog is reached by role, which is rung 2 and already the widest safe match —
    // ds/Modal gives it no id — so it carries no further fallback.
    this.allDialogs = page.getByRole("dialog");
    this.confirmDialog = this.allDialogs.first();
    // Scoped to the dialog on both rungs. ds/Modal gives every modal in the app the same
    // #alert-dialog-title / #cancel ids, so a page-global primary resolves in document
    // order and would read another modal's heading or button whenever two coexist —
    // which is exactly the state allDialogs above exists to detect.
    this.confirmDialogTitle = this.confirmDialog.locator("#alert-dialog-title").or(this.confirmDialog.getByRole("heading")).first();
    this.confirmDialogCancel = this.confirmDialog
      .locator("#cancel")
      .or(this.confirmDialog.getByRole("button", { name: "Cancel" }))
      .first();
    // Scoped to the dialog and deliberately without a name-based fallback: on a
    // destructive action Modal labels this button with the action's own name, which is
    // also the dialog's title, so a wider match could resolve the heading. It is only
    // ever asserted visible, never clicked.
    this.confirmDialogSubmit = this.confirmDialog.locator("#submit").first();

    // The one element on this surface that does carry a testid
    // (ConfirmActionDialog.tsx:50), so it is the primary. Only rendered for an action
    // declared confirmationType: 'strict', which for RDS is `stop` alone.
    this.strictConfirmInput = page.getByTestId("strict-confirm-input").first();
  }

  // ds/FilterDropdown renders its trigger as
  // <button id={`auto-complete-${toKebabCase(id)}`}> (FilterDropdown.jsx:1074-1081).
  filterTrigger(key: keyof typeof FILTER): Locator {
    return this.page
      .locator(`#auto-complete-${FILTER[key]}`)
      .or(this.instancesRoot.getByRole("button", { name: new RegExp(`^${escapeForRegex(FILTER_LABEL[key])}`) }))
      .first();
  }

  // Options are role='option' boxes inside the open MUI Popover
  // (ds/internal/Overlay.tsx:114, 288). Scoped by :visible rather than by the popover
  // paper's MUI class: only one panel is ever open and every other dropdown's options
  // are unmounted.
  filterOption(label: string): Locator {
    return this.visibleFilterOptions()
      .filter({ hasText: new RegExp(`^${escapeForRegex(label)}$`) })
      .first();
  }

  visibleFilterOptions(): Locator {
    return this.page.locator('[role="option"]:visible');
  }

  // FilterDropdown only renders its search box above eight options
  // (FilterDropdown.jsx:1220), so callers must treat its absence as an expected
  // outcome. Scoped to the popover root — the panel has no id or role of its own.
  filterSearchInput(): Locator {
    return this.page.locator(".MuiPopover-root").getByPlaceholder("Search...").first();
  }

  // Takes the header name as INSTANCE_HEADER declares it and matches what CustomTable
  // actually renders — see renderedColumnLabel.
  columnHeader(name: string): Locator {
    return this.instancesTable.getByRole("columnheader", { name: renderedColumnLabel(name), exact: true }).first();
  }

  // One column's cells across the data rows only, in row order. nth-child is 1-based,
  // and CustomTable appends the expand cell after the data cells, so the data columns
  // start at 1 and this offset holds for every one of them.
  columnCells(column: number): Locator {
    return this.instancesRows.locator(`td:nth-child(${column + 1})`);
  }

  stateCells(): Locator {
    return this.columnCells(STATE_COLUMN);
  }

  // CustomTable.jsx:474-476 — one IconButton per data row whose accessible name and
  // aria-expanded both flip with the row's state.
  expandToggle(rowIndex: number): Locator {
    return this.instancesRows.nth(rowIndex).getByRole("button", { name: /^(Expand|Collapse) row$/ });
  }

  // The drill-down panel TabPanel renders for the open row. The div is always in the
  // DOM but carries `hidden` while its tab is unselected and renders no children
  // (CustomTable.jsx:95-96), so :visible is what distinguishes the open panel.
  drilldownPanel(tabName: string): Locator {
    return this.page.locator(`#table-tabpanel-${DRILLDOWN_TABS.indexOf(tabName)}:visible`).first();
  }

  // The drill-down tab strip is a real MUI <Tab> whose label is plain text, so the
  // accessible name is reliable here. CloudAccountTable passes no per-tab id, leaving
  // a11yProps to fall back to `table-tab-${value}` — too generic to key off.
  drilldownTab(tabName: string): Locator {
    return this.page.getByRole("tab", { name: tabName, exact: true }).first();
  }

  // ds/DropdownMenu renders the trigger ds/Button with aria-label='More actions'
  // (Instances.tsx:886) and each item as <li role="menuitem" id={item.id}> where the id
  // is `rds-action-${resourse_id}-${actionId}` (Instances.tsx:879). The resource id is
  // per-row data, so the action is matched on the id's suffix rather than its whole
  // value. getByRole("menuitem", { name }) is deliberately not used — this app nests
  // the label in aria-hidden spans, so it resolves nothing.
  rowActionsTrigger(rowIndex: number): Locator {
    return this.instancesRows.nth(rowIndex).getByRole("button", { name: "More actions" }).first();
  }

  actionMenuItem(actionId: string): Locator {
    return this.page
      .locator(`[role="menuitem"][id$="-${actionId}"]:visible`)
      .or(this.visibleMenuItems().filter({ hasText: new RegExp(`^${escapeForRegex(actionId)}`, "i") }))
      .first();
  }

  visibleMenuItems(): Locator {
    return this.page.locator('[role="menuitem"]:visible');
  }

  // Waits out the fetch. While `loading` is true CustomTable swaps in a skeleton
  // <tbody> that carries no id, so the id'd body being attached means the response has
  // landed. A legitimately empty account renders the empty panel in the body's place,
  // which is why both are accepted — neither is a failure of the code under test.
  async waitForInstances(timeout = 90000): Promise<void> {
    await this.instancesBody.or(this.instancesEmpty).first().waitFor({ state: "attached", timeout });
  }

  // Only for a baseline taken before any interaction. After an action that refetches,
  // assert the expected end state with a retrying expect(...).toHaveCount(n) instead —
  // a plain count races the in-flight request and reads the stale table.
  async instanceRowCount(): Promise<number> {
    await this.waitForInstances();
    return this.instancesRows.count();
  }

  // SearchInput commits on Enter (onEnterPress in Instances.tsx:935) — typing alone
  // filters nothing.
  async searchAndApply(term: string): Promise<void> {
    await this.instancesSearch.click();
    await this.instancesSearch.fill(term);
    await this.instancesSearch.press("Enter");
  }

  // The X only renders while the field has a value, so clearing goes through the field
  // itself and re-commits, matching the onChange('') fast path at Instances.tsx:929-932.
  async clearSearch(): Promise<void> {
    await this.instancesSearch.click();
    await this.instancesSearch.fill("");
    await this.instancesSearch.press("Enter");
  }

  // Opens a toolbar filter and commits `label`, coping with both panel shapes: short
  // lists render no search box at all.
  async chooseFilter(key: keyof typeof FILTER, label: string): Promise<void> {
    const trigger = this.filterTrigger(key);
    await trigger.click();

    // Waits on the panel's options rather than on its search box: the box only exists
    // above eight options, so waiting for it would burn a full timeout on the State
    // filter (seven options) just to conclude it was never coming.
    await this.visibleFilterOptions().first().waitFor({ state: "visible", timeout: 20000 });

    // Options and the search box render in the same pass, so by this point its absence
    // is a settled answer rather than a race, and needs no timeout.
    const search = this.filterSearchInput();
    if (await search.isVisible()) {
      await search.fill(label);
    }

    await this.filterOption(label).click();
    // The trigger shows its label plus the committed value, so this is the signal that
    // the selection landed rather than a fixed pause.
    await expect(trigger).toContainText(label);
  }
}
