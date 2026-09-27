// Not for OSS
import { Page, Locator } from "@playwright/test";
import { CommonLocators } from "../../GlobalLocators";

// Nubi > b-Cortex > Insights > My Usage (app/src/components/llm/LLMConsumptionTab.jsx
// mounted read-only by app/src/ee/components/BCortexModal.jsx). `grep -c data-testid`
// returns 0 for LLMConsumptionTab.jsx, ScopeChip.jsx and BCortexModal.jsx, so rung 1
// is unavailable everywhere here. The tab strips are MUI Tabs (real role=tab), the
// header account filter is a ds/Select carrying id 'account-select', and ds/ProgressBar
// renders role=progressbar. Everything else on the tab is unroled Typography inside
// unlabelled Boxes — KPI tiles, the scope chip, the status pill, the matrix — so those
// are rung 4 text scoped to the b-Cortex dialog, with labelled-anchor walks (never an
// index chain) to reach the tile or row a piece of text belongs to.

export const KPI_TENANT_SPEND = "Monthly spend — tenant";
export const KPI_ACCOUNT_SPEND = "Monthly spend — account";
export const KPI_CONVERSATIONS = "Conversations (month)";
export const KPI_TODAY_SPEND = "Today's spend";
export const KPI_PLACEHOLDER = "No limit configured";
// formatCurrency / formatCount output on either side of the slash, or the placeholder.
export const KPI_VALUE_PATTERN = /^(\$[\d.,]+K? \/ \$[\d.,]+K?|[\d,]+ \/ [\d,]+|No limit configured)$/;
// ScopeChip copy: "Tenant" tenant-wide, "<account> (<provider>)" once an account is in scope.
export const SCOPE_CHIP_PATTERN = /^(Tenant|Tenant · Read-only|.+ \(.+\)|Account [0-9a-f]{8}…)$/;
export const STATUS_PILL_PATTERN = /^(Healthy|Warning|Critical|Exhausted)$/;
export const PERIOD_HEADER_PATTERN = /^Usage for (January|February|March|April|May|June|July|August|September|October|November|December) \d{4}$/;
export const MODULE_EVENT_ANALYSIS = "Event Analysis";
export const ALL_ACCOUNTS_LABEL = "All accounts";

export class MyUsageLocators extends CommonLocators {
  readonly bCortexDialog: Locator;
  readonly insightsGroupTab: Locator;
  readonly memoryGroupTab: Locator;
  readonly digestsTab: Locator;
  readonly myUsageTab: Locator;
  readonly bCortexDisabledPanel: Locator;
  readonly usageHeader: Locator;
  readonly headerRow: Locator;
  readonly scopeChip: Locator;
  readonly statusPill: Locator;
  readonly description: Locator;
  readonly addBudgetBtn: Locator;
  readonly accountSelect: Locator;
  readonly openListbox: Locator;
  readonly realAccountOption: Locator;
  readonly matrix: Locator;
  readonly matrixEventAnalysisHeader: Locator;
  readonly matrixChatHeader: Locator;
  readonly matrixScopeLabels: Locator;

  constructor(page: Page) {
    super(page);

    // Matched by attribute, not getByRole("dialog"): while the account picker's
    // panel is open MUI marks the b-Cortex dialog aria-hidden and a role lookup
    // would report it closed. Single-rung on purpose — every scoped fallback below
    // hangs off it.
    this.bCortexDialog = page.locator('[role="dialog"]').filter({ hasText: "b-Cortex" }).first();

    // BCortexModal renders its strips through shared/navigation/Tabs (MUI Tabs).
    // Insights, Memory and Digests carry MUI icons, so their names are the bare
    // label; My Usage's icon is an image file whose alt repeats the label, so its
    // accessible name is "My Usage My Usage" and only a loose match reaches it.
    this.insightsGroupTab = page.getByRole("tab", { name: "Insights", exact: true });
    this.memoryGroupTab = page.getByRole("tab", { name: "Memory", exact: true });
    this.digestsTab = page.getByRole("tab", { name: "Digests", exact: true });
    this.myUsageTab = page
      .getByRole("tab", { name: "My Usage" })
      .or(this.bCortexDialog.locator('[role="tab"]').filter({ hasText: /^My Usage$/ }))
      .first();

    // Rendered in place of the whole tab when the tenant's b-Cortex flag is off.
    this.bCortexDisabledPanel = this.bCortexDialog.getByText("b-Cortex not enabled for your tenant", { exact: true }).first();

    // "Usage for <Month YYYY>" is the tab's own title and the only text on the
    // surface that starts this way, so it is the "the tab mounted" signal; it
    // reads "Usage for —" until the budget-status read lands.
    this.usageHeader = this.bCortexDialog.getByText(/^Usage for /).first();
    // The header's Stack holds the title, the scope chip, the status pill and the
    // optional Today chip, so walking up from the title scopes those three.
    this.headerRow = this.usageHeader.locator("xpath=..");
    // ScopeChip renders a non-interactive ds/Chip (a span): "Tenant" tenant-wide,
    // "<account> (<provider>)" once narrowed. Scoped to the header row so the
    // matrix's own "Tenant" scope label cannot satisfy it.
    this.scopeChip = this.headerRow.locator("span").filter({ hasText: SCOPE_CHIP_PATTERN }).first();
    this.statusPill = this.headerRow.getByText(STATUS_PILL_PATTERN).first();
    this.description = this.bCortexDialog.getByText(/Budget and consumption metrics for LLM-powered services/).first();

    // Hidden by the readOnly mount; asserted absent, never clicked.
    this.addBudgetBtn = this.bCortexDialog.getByRole("button", { name: "Add Budget", exact: true });

    // b-Cortex's header AccountSelect is a ds/Select whose trigger's accessible
    // name is the selected option — tenant data — so the id it stamps is the
    // stable primary and the name form is only the fallback.
    this.accountSelect = this.bCortexDialog.locator("#account-select").or(this.bCortexDialog.getByRole("button", { name: /accounts?/i })).first();
    // ds/Select's panel is a MUI Menu whose list carries role=listbox.
    this.openListbox = page.locator('[role="listbox"]:visible');
    // Any account entry other than the synthetic All accounts one.
    this.realAccountOption = this.openListbox.locator('[role="option"]').filter({ hasNotText: ALL_ACCOUNTS_LABEL }).first();

    // UsageMatrix: the "Scope" header cell is its only fixed text, so the matrix
    // Box is reached by walking up from it (cell → header grid → matrix).
    this.matrix = this.bCortexDialog.getByText("Scope", { exact: true }).first().locator("xpath=../..");
    this.matrixEventAnalysisHeader = this.matrix.getByText(MODULE_EVENT_ANALYSIS, { exact: true }).first();
    // The second module column is "<assistantName> Chat" and the assistant name is
    // branding, so only the suffix is pinned.
    this.matrixChatHeader = this.matrix.getByText(/ Chat$/).first();
    // One "Tenant" / "Account" label per scope row, so their count is the row count.
    this.matrixScopeLabels = this.matrix.getByText(/^(Tenant|Account)$/);
  }

  // A KPI tile, reached from its own label: label <p> → title row → tile Box.
  kpiTile(label: string): Locator {
    return this.bCortexDialog.getByText(label, { exact: true }).first().locator("xpath=../..");
  }

  // The tile's value line — either "usage / limit" or the placeholder.
  kpiValue(label: string): Locator {
    return this.kpiTile(label).getByText(KPI_VALUE_PATTERN).first();
  }

  kpiProgress(label: string): Locator {
    return this.kpiTile(label).getByRole("progressbar");
  }

  kpiPlaceholder(label: string): Locator {
    return this.kpiTile(label).getByText(KPI_PLACEHOLDER, { exact: true });
  }

  // A matrix row, reached from its scope label: label <p> → scope cell → row Box.
  matrixRow(scopeLabel: "Tenant" | "Account"): Locator {
    return this.matrix.getByText(scopeLabel, { exact: true }).first().locator("xpath=../..");
  }

  // The first account entry that is neither the All accounts one nor the account
  // currently in scope — the target for a switch that must change the view.
  otherAccountOption(current: string): Locator {
    return this.openListbox.locator('[role="option"]').filter({ hasNotText: ALL_ACCOUNTS_LABEL }).filter({ hasNotText: current }).first();
  }

  // One option in the open account picker. Exact text so "All accounts" cannot
  // match an account whose name contains it; visible-only so a closing panel
  // cannot satisfy it. `[role="option"]` as CSS: ds/Select marks the rows with the
  // attribute, and the aria-hidden dialog the open panel causes would empty a
  // role lookup.
  accountOption(label: string): Locator {
    return this.page
      .locator('[role="option"]:visible')
      .filter({ hasText: new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) })
      .first();
  }
}
