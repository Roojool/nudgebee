// Not for OSS
import { Page, Locator } from "@playwright/test";
import { NotificationLocators } from "./NotificationLocators";
import { RULES_TABLE } from "./notificationsModuleConstants";

// Extends NotificationLocators rather than restating it: the rule editor's name input,
// type chips, Do choice, Add rule button and success toasts are declared there and
// are inherited unchanged. Only the listing surface, its filter chips, search and
// the delete dialog are new here.
export class NotificationsModuleLocators extends NotificationLocators {
  readonly listingRoot: Locator;
  readonly rulesTable: Locator;
  readonly rulesTableBody: Locator;
  readonly ruleRows: Locator;

  readonly allFilterChip: Locator;
  readonly weeklyDigestFilterChip: Locator;
  readonly searchInput: Locator;
  readonly testNotificationBtn: Locator;
  readonly fallbackRow: Locator;

  readonly ruleDialog: Locator;
  readonly ruleSubmitBtn: Locator;
  readonly ruleCancelBtn: Locator;
  readonly nameError: Locator;

  readonly deleteDialog: Locator;
  readonly deleteConfirmBtn: Locator;
  readonly deleteCancelBtn: Locator;

  readonly accessUsersTab: Locator;
  readonly auditLogMenuItem: Locator;
  readonly auditsTab: Locator;

  constructor(page: Page) {
    super(page);

    this.listingRoot = page.locator("#notification-container").or(page.locator(`#${RULES_TABLE}`)).first();

    this.rulesTable = page.locator(`#${RULES_TABLE}`).or(this.listingRoot.getByRole("table")).first();

    this.rulesTableBody = page.locator(`#${RULES_TABLE}-body`).or(this.rulesTable.locator("tbody")).first();

    // showExpandable={false} on this table, so there is no second <tr> per data row and
    // a plain row count is accurate. Requiring a <td> still excludes the header row.
    this.ruleRows = this.rulesTableBody.locator("tr:has(td)");

    // One chip per notification type, each with its rule count.
    this.allFilterChip = page.locator("#filter-all");
    this.weeklyDigestFilterChip = page.locator("#filter-weekly_digest");
    this.searchInput = page.locator("#search-rules");
    this.testNotificationBtn = page.locator("#test-notification");
    // "When no rule matches" is the last row on every page of the listing.
    this.fallbackRow = this.ruleRows.filter({ has: page.locator("#fallback") });

    this.ruleDialog = page.locator('[role="dialog"]').filter({ has: page.locator("#notificationName") }).first();
    this.ruleSubmitBtn = page.locator("#save-rule");
    this.ruleCancelBtn = page.locator("#cancel-rule");
    // The editor shows why a save was refused next to its buttons.
    this.nameError = page.locator("#rule-error");

    this.deleteDialog = page.locator('[role="dialog"]').filter({ hasText: "Delete rule" }).first();
    this.deleteConfirmBtn = this.deleteDialog.locator("#submit").or(this.deleteDialog.getByRole("button", { name: /^Delete$/i })).first();
    this.deleteCancelBtn = this.deleteDialog.locator("#cancel").or(this.deleteDialog.getByRole("button", { name: /^Cancel$/i })).first();

    // Access & Users is the top-level tab Audit Log now lives under (AnchorComponent's
    // own anchor-tab-<id> button — id is pinned to "AccessUsers" in
    // user-management/index.jsx since the display name has a space and an
    // ampersand, neither safe in a bare CSS id selector). Audit Log itself is
    // one of its sub-tabs, not reachable until this top-level tab is open — see
    // the two-step click in NotificationsModule.spec.ts.
    this.accessUsersTab = page.locator("#anchor-tab-AccessUsers").or(page.getByRole("link", { name: "Access & Users" })).first();
    // AnchorComponent renders no data-testid; its hover-menu MenuItem id is `dropdown-${item.id}`.
    this.auditLogMenuItem = page
      .locator('[role="menuitem"]#dropdown-audit-log:visible')
      .or(page.locator('[role="menuitem"]:visible').filter({ hasText: "Audit Log" }))
      .first();
    // Sub-tab of Access & Users, rendered by the shared Tabs.jsx (a real MUI Tab)
    // — id comes from a11yProps(value, opt.id) where opt.id is "audit-log".
    this.auditsTab = page.locator("#audit-log").or(page.getByRole("tab", { name: "Audit Log", exact: true })).first();
  }

  // Rows are matched on the rule name, which this suite generates unique per rule — so
  // this never straddles two rows and never touches a pre-existing rule.
  rowByName(name: string): Locator {
    return this.ruleRows.filter({ hasText: name });
  }

  // Delete sits in each row's ⋮ menu, id `${name}-delete`; the menu opens from `${name}-more`.
  // The generated name contains spaces, so the attribute form is required.
  async openDeleteForRule(name: string): Promise<void> {
    await this.rowByName(name).locator(`[id="${name}-more"]`).click();
    await this.page.locator(`[id="${name}-delete"]`).click();
  }
}
