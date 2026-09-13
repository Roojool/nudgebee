// Not for OSS
import { Page, Locator } from "@playwright/test";
import { CommonLocators } from "../../GlobalLocators";

// Nubi > AI & Tools > Gateway (app/src/ee/components/gateway-config/GatewayConfigTab.tsx
// and its four sections). Every action button carries a data-testid — New rule /
// New quota, the per-row edit/delete/remap pair, the dialog Create/Save and the
// delete confirms — so those are rung 1. The form controls are ds primitives
// given an explicit id: ds/Input wires a real <label htmlFor>, so its inputs are
// taken at rung 2 by name with the id as the scoped fallback. ds/Select renders
// <label htmlFor> against a <button> whose accessible name is its own text —
// the selected option, i.e. tenant data — so its triggers are rung 3 by
// necessity, with a walk from their own label as the fallback. The tab shell
// (GatewayConfigTab.tsx) and DataPrivacySection.tsx render zero data-testid, so
// their ids are the highest rung that exists there.
export class GatewayConfigLocators extends CommonLocators {
  readonly gatewayTab: Locator;
  readonly agentsTab: Locator;
  readonly root: Locator;
  readonly sectionToggle: Locator;
  readonly tiersOption: Locator;
  readonly routingOption: Locator;
  readonly quotasOption: Locator;
  readonly privacyOption: Locator;
  readonly loadErrorBanner: Locator;
  readonly tiersTableBody: Locator;
  readonly tiersEmptyState: Locator;
  readonly firstTierAliasCell: Locator;
  readonly tierDialog: Locator;
  readonly tierPlatformDefault: Locator;
  readonly tierProviderSelect: Locator;
  readonly tierModelInput: Locator;
  readonly tierSaveBtn: Locator;
  readonly tierCancelBtn: Locator;
  readonly newRuleBtn: Locator;
  readonly routingTableBody: Locator;
  readonly routingEmptyState: Locator;
  readonly ruleDialog: Locator;
  readonly newRuleDialog: Locator;
  readonly editRuleDialog: Locator;
  readonly rulePriorityInput: Locator;
  readonly ruleEnabledSwitch: Locator;
  readonly ruleMatchModelInput: Locator;
  readonly ruleTargetProviderSelect: Locator;
  readonly ruleTargetModelInput: Locator;
  readonly ruleTargetModelError: Locator;
  readonly ruleSaveBtn: Locator;
  readonly ruleCancelBtn: Locator;
  readonly deleteRuleDialog: Locator;
  readonly confirmDeleteRuleBtn: Locator;
  readonly cancelDeleteRuleBtn: Locator;
  readonly newQuotaBtn: Locator;
  readonly quotaTableBody: Locator;
  readonly quotaEmptyState: Locator;
  readonly quotaDialog: Locator;
  readonly newQuotaDialog: Locator;
  readonly quotaMetricSelect: Locator;
  readonly quotaPeriodSelect: Locator;
  readonly quotaLimitInput: Locator;
  readonly quotaEnabledSwitch: Locator;
  readonly quotaSaveBtn: Locator;
  readonly quotaCancelBtn: Locator;
  readonly deleteQuotaDialog: Locator;
  readonly confirmDeleteQuotaBtn: Locator;
  readonly cancelDeleteQuotaBtn: Locator;
  readonly captureBodyToggle: Locator;
  readonly dlpModeSelect: Locator;
  readonly privacySaveBtn: Locator;
  readonly openListbox: Locator;

  constructor(page: Page) {
    super(page);

    // The Nubi panel entry point and its "AI & Tools" button are not redeclared —
    // tests/nubi/nubiLocators.ts owns them and openGatewayTab() drives that class.
    // Gateway is one of AI & Tools' sub-tabs (aiToolsConfig.js, shown only while
    // UI_ENABLE_LLM_GATEWAY is on), and Agents is the sibling remountGatewayTab()
    // switches away to. AIToolsModal renders its strip through
    // shared/navigation/Tabs (MUI Tabs), so every tab is a real role=tab.
    // Tabs renders an SVG-file icon as <img alt={text}>, so a tab with one
    // (Agents) is named "Agents Agents" — exact matching would miss it — while a
    // MUI-icon tab (Gateway) carries only its label and can be matched exactly.
    this.gatewayTab = page.getByRole("tab", { name: "Gateway", exact: true });
    this.agentsTab = page.getByRole("tab", { name: "Agents" });

    // GatewayConfigTab's own root Box. Deliberately id-only: it is a plain div
    // with no role, and it is what every scoped fallback below hangs off, so a
    // wider fallback here would widen all of them at once.
    this.root = page.locator("#gateway-config-root");

    // ds/ToggleGroup wraps its options in role=group named by ariaLabel, and a
    // single-selection option is role=radio carrying aria-checked — not a class.
    this.sectionToggle = this.root.getByRole("group", { name: "Gateway configuration section" });
    this.tiersOption = this.sectionToggle.getByRole("radio", { name: "Tiers", exact: true });
    this.routingOption = this.sectionToggle.getByRole("radio", { name: "Routing rules", exact: true });
    this.quotasOption = this.sectionToggle.getByRole("radio", { name: "Quotas", exact: true });
    this.privacyOption = this.sectionToggle.getByRole("radio", { name: "Data & privacy", exact: true });

    // ds/Banner renders role=alert for the critical tone, which is what every
    // section shows when its llm_gateway_* list call fails. The text filter keeps
    // this off Data & privacy's warning banner, which shares the role.
    this.loadErrorBanner = this.root.getByRole("alert").filter({ hasText: /fail|could not|error|unreachable|missing/i }).first();

    // CustomTable stamps `${id}-body` on its data tbody only — while loading it
    // swaps in a separate skeleton tbody that carries no id, so a `tbody` fallback
    // would settle every wait on the loading state. Id-only on purpose: the rows
    // then simply do not exist until the data lands.
    this.tiersTableBody = page.locator("#gateway-tiers-table-body");
    this.routingTableBody = page.locator("#gateway-routing-table-body");
    this.quotaTableBody = page.locator("#gateway-quota-table-body");

    // CustomTable renders EmptyData with id={tableId}, which stamps the heading
    // as `${tableId}-no-data`; the heading text is the section's emptyHeading.
    this.tiersEmptyState = page.locator("#gateway-tiers-table-no-data").or(this.root.getByRole("heading", { name: "No tiers" })).first();
    this.routingEmptyState = page.locator("#gateway-routing-table-no-data").or(this.root.getByRole("heading", { name: "No routing rules" })).first();
    this.quotaEmptyState = page.locator("#gateway-quota-table-no-data").or(this.root.getByRole("heading", { name: "No quotas" })).first();

    // The alias sits in its own <code> cell, so the first row's alias is read
    // from there rather than from the row's whole text.
    this.firstTierAliasCell = this.tierRows().first().locator("code").first();

    // ds/Modal portals to <body>, so every dialog below is a DOM sibling of the
    // AI & Tools dialog rather than a descendant, and all of them stamp the same
    // #alert-dialog-title id — hence the filter on each dialog's own title
    // element. Filtering on body text instead would resolve "New quota" to the
    // AI & Tools dialog, which holds the button of that name, in document order.
    this.tierDialog = this.dialogWithTitle(/^Remap /);
    this.tierPlatformDefault = this.tierDialog.getByText(/Platform default:/).first();
    this.tierProviderSelect = this.tierDialog.locator("#gateway-tier-provider").or(this.selectTriggerByLabel(this.tierDialog, "Provider")).first();
    this.tierModelInput = this.inputOf(this.tierDialog, "textbox", "Model", "#gateway-tier-model");
    this.tierSaveBtn = page.getByTestId("gateway-tier-save-btn").or(this.dialogBtn(this.tierDialog, "Save")).first();
    this.tierCancelBtn = this.dialogBtn(this.tierDialog, "Cancel");

    this.newRuleBtn = page.getByTestId("gateway-rule-new-btn").or(this.root.getByRole("button", { name: "New rule" })).first();
    this.ruleDialog = this.dialogWithTitle(/^(New|Edit) routing rule$/);
    this.newRuleDialog = this.dialogWithTitle(/^New routing rule$/);
    this.editRuleDialog = this.dialogWithTitle(/^Edit routing rule$/);

    // "Model" is exact so it cannot drift onto the Target model field, whose
    // label starts the same and reads "Target model", "Target model (required)"
    // or "Replacement model (required)" depending on the form state.
    this.rulePriorityInput = this.inputOf(this.ruleDialog, "spinbutton", "Priority", "#gateway-rule-priority-input");
    this.ruleMatchModelInput = this.inputOf(this.ruleDialog, "textbox", "Model", "#gateway-rule-match-model");
    this.ruleTargetModelInput = this.inputOf(this.ruleDialog, "textbox", /^Target model/, "#gateway-rule-target-model");
    this.ruleEnabledSwitch = this.enabledSwitchOf(this.ruleDialog, "#gateway-rule-enabled-switch");
    this.ruleTargetProviderSelect = this.ruleDialog.locator("#gateway-rule-target-provider").or(this.selectTriggerByLabel(this.ruleDialog, "Target provider")).first();

    // ds/Input renders its validation message as <span id={`${inputId}-error`}
    // role="alert">, so the id is exact and the role is the scoped fallback.
    this.ruleTargetModelError = this.ruleDialog.locator("#gateway-rule-target-model-error").or(this.ruleDialog.getByRole("alert").filter({ hasText: /target model is required/i })).first();

    this.ruleSaveBtn = page.getByTestId("gateway-rule-save-btn").or(this.createOrSaveBtnOf(this.ruleDialog)).first();
    this.ruleCancelBtn = this.dialogBtn(this.ruleDialog, "Cancel");

    this.deleteRuleDialog = this.dialogWithTitle(/^Delete routing rule$/);
    this.confirmDeleteRuleBtn = page.getByTestId("gateway-rule-confirm-delete-btn").or(this.dialogBtn(this.deleteRuleDialog, "Delete")).first();
    this.cancelDeleteRuleBtn = this.dialogBtn(this.deleteRuleDialog, "Cancel");

    this.newQuotaBtn = page.getByTestId("gateway-quota-new-btn").or(this.root.getByRole("button", { name: "New quota" })).first();
    this.quotaDialog = this.dialogWithTitle(/^(New|Edit) quota$/);
    this.newQuotaDialog = this.dialogWithTitle(/^New quota$/);
    this.quotaMetricSelect = this.quotaDialog.locator("#gateway-quota-metric").or(this.selectTriggerByLabel(this.quotaDialog, "Metric")).first();
    this.quotaPeriodSelect = this.quotaDialog.locator("#gateway-quota-period").or(this.selectTriggerByLabel(this.quotaDialog, "Period")).first();

    // The Limit label carries the metric's unit in brackets, so it is a prefix match.
    this.quotaLimitInput = this.inputOf(this.quotaDialog, "spinbutton", /^Limit/, "#gateway-quota-limit");
    this.quotaEnabledSwitch = this.enabledSwitchOf(this.quotaDialog, "#gateway-quota-enabled-switch");
    this.quotaSaveBtn = page.getByTestId("gateway-quota-save-btn").or(this.createOrSaveBtnOf(this.quotaDialog)).first();
    this.quotaCancelBtn = this.dialogBtn(this.quotaDialog, "Cancel");

    this.deleteQuotaDialog = this.dialogWithTitle(/^Delete quota$/);
    this.confirmDeleteQuotaBtn = page.getByTestId("gateway-quota-confirm-delete-btn").or(this.dialogBtn(this.deleteQuotaDialog, "Delete")).first();
    this.cancelDeleteQuotaBtn = this.dialogBtn(this.deleteQuotaDialog, "Cancel");

    // DataPrivacySection renders zero data-testid. The capture switch is given no
    // label, so it has no accessible name at all and the id is the only handle;
    // the DLP trigger is a ds/Select (see the class note) with a label walk as
    // its fallback; Save reads "Saving…" while a write is in flight.
    this.captureBodyToggle = this.root.locator("#gateway-capture-body-toggle");
    this.dlpModeSelect = this.root.locator("#gateway-dlp-mode-select").or(this.selectTriggerByLabel(this.root, "Mode")).first();
    this.privacySaveBtn = this.root.locator("#gateway-data-privacy-save").or(this.root.getByRole("button", { name: /^Sav(e changes|ing)/ })).first();

    // ds/Select's panel is a MUI Menu whose list carries role=listbox, so its
    // presence is the "picker is open" signal and its absence the "closed" one.
    this.openListbox = page.locator('[role="listbox"]:visible');
  }

  // The dialog whose own ds/Modal title element reads `title`. Matched by
  // attribute rather than getByRole("dialog"): a ds/Select panel opened inside
  // a dialog makes MUI aria-hide that dialog, and a role lookup would then read
  // "the dialog closed" while it is still on screen.
  private dialogWithTitle(title: RegExp): Locator {
    return this.page
      .locator('[role="dialog"]')
      .filter({ has: this.page.locator("#alert-dialog-title").filter({ hasText: title }) })
      .first();
  }

  private dialogBtn(dialog: Locator, name: string): Locator {
    return dialog.getByRole("button", { name, exact: true });
  }

  // The primary action reads "Create" in add mode and "Save" in edit mode.
  private createOrSaveBtnOf(dialog: Locator): Locator {
    return dialog.getByRole("button", { name: /^(Create|Save)$/ });
  }

  // ds/Input wires a real <label htmlFor>, and a type=number input is a
  // spinbutton, so every form field has an accessible name; the id the section
  // passes through is the scoped fallback.
  private inputOf(dialog: Locator, role: "textbox" | "spinbutton", name: string | RegExp, id: string): Locator {
    return dialog.getByRole(role, { name, exact: true }).or(dialog.locator(id)).first();
  }

  // ds/Switch sets inputProps['aria-label'] from its `label`, and MUI renders
  // it as a checkbox input, not role=switch. Exact: the rule dialog's Block and
  // Deprecation switches carry longer labels.
  private enabledSwitchOf(dialog: Locator, id: string): Locator {
    return dialog.getByRole("checkbox", { name: "Enabled", exact: true }).or(dialog.locator(id)).first();
  }

  // ds/Select renders its <label> as the first child of the same column Box as
  // the trigger <button>, so walking to the label's next button sibling reaches
  // the trigger without depending on the id or on the selected option's text.
  private selectTriggerByLabel(container: Locator, label: string): Locator {
    return container.locator("label", { hasText: label }).locator("xpath=following-sibling::button[1]");
  }

  // Every data row currently rendered in one table body. CustomTable renders no
  // rows at all when the result is empty, so a count of 0 is meaningful.
  private rowsOf(body: Locator): Locator {
    return body.getByRole("row").or(body.locator("tr"));
  }

  // A non-interactive ds/Chip renders as a plain span, so the status columns'
  // chips are a scoped exact-text match by necessity.
  private statusChipOf(row: Locator): Locator {
    return row.getByText(/^(Enabled|Disabled)$/).first();
  }

  // One option in whichever ds/Select panel is open. Exact text so "Any" cannot
  // match "Any user", and visible-only so a stale, closing panel cannot satisfy it.
  selectOption(label: string): Locator {
    return this.page
      .locator('[role="option"]:visible')
      .filter({ hasText: new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) })
      .first();
  }

  tierRows(): Locator {
    return this.rowsOf(this.tiersTableBody);
  }

  ruleRows(): Locator {
    return this.rowsOf(this.routingTableBody);
  }

  quotaRows(): Locator {
    return this.rowsOf(this.quotaTableBody);
  }

  // The tier alias sits in its own <code> cell, so the row is matched on that
  // rather than on loose text a description column could also contain.
  tierRow(alias: string): Locator {
    return this.tierRows().filter({ has: this.page.locator("code", { hasText: new RegExp(`^${alias}$`) }) }).first();
  }

  remapTierBtn(alias: string): Locator {
    return this.page.getByTestId(`gateway-tier-edit-btn-${alias}`).or(this.tierRow(alias).getByRole("button", { name: `Remap ${alias}` })).first();
  }

  tierSourceChip(alias: string): Locator {
    return this.tierRow(alias).getByText(/^(Default|Custom)$/).first();
  }

  // One rule row, matched inside the table body so a model that also appears in
  // an open dialog cannot satisfy it.
  ruleRow(model: string): Locator {
    return this.ruleRows().filter({ hasText: model }).first();
  }

  // The per-row testids carry the server-assigned rule id, which this suite only
  // learns after the write, so a row-scoped prefix match is the rung-1 form
  // available here; the aria-label is the fallback in the same row.
  editRuleBtn(model: string): Locator {
    const row = this.ruleRow(model);
    return row.locator('[data-testid^="gateway-rule-edit-btn-"]').or(row.getByRole("button", { name: "Edit routing rule" })).first();
  }

  deleteRuleBtn(model: string): Locator {
    const row = this.ruleRow(model);
    return row.locator('[data-testid^="gateway-rule-delete-btn-"]').or(row.getByRole("button", { name: "Delete routing rule" })).first();
  }

  ruleStatusChip(model: string): Locator {
    return this.statusChipOf(this.ruleRow(model));
  }

  // Quotas carry no name; the Limit cell is the only run-unique value a row
  // shows, rendered through toLocaleString — hence the separator-tolerant pattern.
  quotaRow(limit: RegExp): Locator {
    return this.quotaRows().filter({ hasText: limit }).first();
  }

  deleteQuotaBtn(limit: RegExp): Locator {
    const row = this.quotaRow(limit);
    return row.locator('[data-testid^="gateway-quota-delete-btn-"]').or(row.getByRole("button", { name: "Delete quota" })).first();
  }

  quotaStatusChip(limit: RegExp): Locator {
    return this.statusChipOf(this.quotaRow(limit));
  }
}
