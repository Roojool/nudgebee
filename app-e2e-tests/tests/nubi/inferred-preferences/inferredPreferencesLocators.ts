// Not for OSS
import { Page, Locator } from "@playwright/test";
import { CommonLocators } from "../../GlobalLocators";

// Nubi > b-Cortex > Preferences > Inferred (app/src/ee/components/memory2/InferredPreferencesTab.jsx).
// `grep -c data-testid` returns 0 for that file and for shared.jsx (TabHeader), ds/Button,
// ds/Modal, ds/EmptyState and ds/Link. ds/Chip and ds/Card each forward a data-testid they are
// given, but this tab passes neither one, so nothing reaches the DOM and there is no rung-1
// handle anywhere on this surface.
// Rung 2 covers everything carrying an accessible name: both tab strips, the per-row Keep /
// Unkeep / Discard controls, the confirm dialogs' own buttons, and the empty state (ds/EmptyState
// renders role=status). The account picker is rung 3 — AccountSelect renders an id and neither a
// testid nor an accessible name. The header title, the row value, the Kept chip and the dialog
// copy carry no role at all and are matched by their own text, scoped to the dialog holding them.
// Several locators below are single-rung on purpose: the string each one matches IS the assertion
// its case exists to make, so a wider fallback would let a different element satisfy the very
// check the test was written for.
export class InferredPreferencesLocators extends CommonLocators {
  readonly memoryGroupTab: Locator;
  readonly preferencesGroupTab: Locator;
  readonly inferredTab: Locator;
  readonly typedTab: Locator;
  readonly patternsTab: Locator;
  readonly decisionsTab: Locator;
  readonly bCortexDialog: Locator;
  readonly closeDialogBtn: Locator;
  readonly tabHeaderTitle: Locator;
  readonly tabDescription: Locator;
  readonly emptyState: Locator;
  readonly keepButtons: Locator;
  readonly unkeepButtons: Locator;
  readonly discardButtons: Locator;
  readonly rowActionButtons: Locator;
  readonly keptChips: Locator;
  readonly accountSelect: Locator;
  readonly typedSaveBtn: Locator;
  readonly typedResetBtn: Locator;
  readonly memoryScopeToggle: Locator;
  readonly keepDialog: Locator;
  readonly keepDialogConfirmBtn: Locator;
  readonly keepDialogCancelBtn: Locator;
  readonly discardDialog: Locator;
  readonly discardDialogConfirmBtn: Locator;
  readonly discardDialogCancelBtn: Locator;
  readonly bCortexDisabledPanel: Locator;
  readonly notificationsRegion: Locator;

  constructor(page: Page) {
    super(page);

    // The Nubi panel entry point and its "b-Cortex" rail button are not redeclared here —
    // tests/nubi/nubiLocators.ts owns them and openInferredTab() drives it.
    // BCortexModal renders both strips through shared/navigation/Tabs (MUI Tabs), so every tab
    // is a real role=tab even while scrolled out of view. No text fallback on the group tabs:
    // "Preferences" is also the first word of this tab's own header, so a text match could
    // return the heading instead of the tab.
    this.memoryGroupTab = page.getByRole("tab", { name: "Memory", exact: true });
    this.preferencesGroupTab = page.getByRole("tab", { name: "Preferences", exact: true });
    // Inferred is the Preferences group's default sub-tab and Typed its sibling
    // (BCortexModal.jsx PREFERENCES_SUB_TABS_CONFIG). Typed is used to remount Inferred.
    this.inferredTab = page.getByRole("tab", { name: "Inferred", exact: true });
    this.typedTab = page.getByRole("tab", { name: "Typed", exact: true });
    // Memory's own sub-tabs, used to prove the strip is replaced when the group changes and to
    // reach a sub-tab that does render the account picker.
    this.patternsTab = page.getByRole("tab", { name: "Patterns", exact: true });
    this.decisionsTab = page.getByRole("tab", { name: "Decisions", exact: true });

    // Every locator below scopes to the b-Cortex dialog so a matching string on the page behind
    // the modal cannot satisfy it. Deliberately single-rung: this is what the rest of the file
    // scopes to, so a fallback here would widen all of them at once.
    this.bCortexDialog = this.dialogTitled("b-Cortex");

    // ds/Modal's own close control. Scoped to the b-Cortex dialog so it cannot resolve to a
    // close button on the page behind it.
    this.closeDialogBtn = this.bCortexDialog
      .getByRole("button", { name: "Close", exact: true })
      .or(this.bCortexDialog.locator("#close-modal-btn"))
      .first();

    // TabHeader's title is the one string only this sub-tab renders, so it doubles as the
    // "Inferred is showing" signal every case and the remount helper wait on. Matched exactly
    // and single-rung for that reason.
    this.tabHeaderTitle = this.bCortexDialog.getByText("Preferences — inferred from your chats", { exact: true }).first();

    // The description interpolates the tenant's assistant name, so only the brand-free half is
    // matched — app/CLAUDE.md forbids pinning that name in copy, and a partner tenant renders a
    // different one. This clause is the tab's whole contract: Keep is reversible via Unkeep.
    this.tabDescription = this.bCortexDialog.getByText("marked Kept, and you can Unkeep it at any time").first();

    // ds/EmptyState renders role=status, so the empty queue is reachable at rung 2. The fallback
    // matches the same title by text inside the same dialog, for the aria-hidden reason above.
    this.emptyState = this.bCortexDialog
      .getByRole("status")
      .filter({ hasText: "No inferred preferences yet" })
      .or(this.bCortexDialog.getByText("No inferred preferences yet", { exact: true }))
      .first();

    // The per-row controls. Scoping these to bCortexDialog is load-bearing rather than tidy: the
    // confirm dialogs are their own ds/Modal portals — siblings of this dialog, not nodes inside
    // it — and each carries a button with the same accessible name as the row control that opened
    // it. The scope is the only thing keeping the two apart.
    // exact:true is load-bearing too: getByRole matches `name` as a substring by default, so a
    // loose "Keep" would also collect every Unkeep button on the tab.
    this.keepButtons = this.keepButtonsIn(this.bCortexDialog);
    this.unkeepButtons = this.unkeepButtonsIn(this.bCortexDialog);
    // Icon-only, so ds/Button takes its name from the aria-label it is given.
    this.discardButtons = this.discardButtonsIn(this.bCortexDialog);

    // Every row renders exactly one of Keep or Unkeep, so this union is one entry per row and its
    // count is the row count — which is also the only end-of-load signal the tab offers.
    this.rowActionButtons = this.keepButtons.or(this.unkeepButtons);

    this.keptChips = this.keptChipIn(this.bCortexDialog);

    // AccountSelect renders id={id || 'account-select'} and BCortexModal passes no id. ds/Select
    // renders no testid and its trigger has no accessible name, so rung 3 is the highest
    // available. Scoped to the dialog: the page behind the modal carries its own picker with this
    // same id. Deliberately no fallback — the only wider match left is the combobox role, and the
    // case using this asserts the picker is ABSENT on this sub-tab, so a fallback that could
    // resolve to any other select on the surface would turn that assertion green against the
    // wrong element.
    this.accountSelect = this.bCortexDialog.locator("#account-select");

    // Asserted absent on Inferred and present on the Typed sibling, which is what keeps the
    // absence half honest: a selector that was simply wrong would report "absent" on both.
    // Single-rung for the same reason — a fallback wide enough to find some other Save on the
    // surface would pass the absence assertion against the wrong element.
    this.typedSaveBtn = this.bCortexDialog.getByRole("button", { name: "Save", exact: true });
    this.typedResetBtn = this.bCortexDialog.getByRole("button", { name: "Reset", exact: true });
    // ds/ToggleGroup renders role=group carrying the ariaLabel it is given.
    this.memoryScopeToggle = this.bCortexDialog.getByRole("group", { name: "Memory scope" });

    // Each confirm dialog is its own ds/Modal — a sibling portal of b-Cortex rather than a node
    // inside it — so both are reached from the page and not from bCortexDialog. Separated by their
    // ds/Modal titles, the only strings that tell the two apart: neither title is a substring of
    // anything the b-Cortex dialog renders behind them.
    this.keepDialog = this.dialogTitled("Keep preference");
    this.discardDialog = this.dialogTitled("Discard preference");

    this.keepDialogConfirmBtn = this.keepButtonsIn(this.keepDialog);
    // Declared so the cancel case can assert the dialog offered the action and then left it
    // unpressed. This suite NEVER clicks it: a discard destroys an inferred preference belonging
    // to a shared dev user, and the product offers no way to put one back.
    this.discardDialogConfirmBtn = this.discardButtonsIn(this.discardDialog);
    // Not CommonLocators.cancelBtn: that one is page-wide and unscoped, so with b-Cortex open
    // behind these dialogs it can resolve to a Cancel in the dialog underneath.
    this.keepDialogCancelBtn = this.cancelButtonIn(this.keepDialog);
    this.discardDialogCancelBtn = this.cancelButtonIn(this.discardDialog);

    // Rendered in place of the whole tab when the tenant's b-Cortex flag is off
    // (BCortexDisabled.jsx). Asserted against so a disabled module reports itself by name instead
    // of surfacing as every control being absent. Matched on its title, not on the one testid
    // that file carries: that is its "view legacy memory" button, which only renders while a
    // onOpenLegacy callback is passed, and BCortexModal's own wrapper passes a no-op.
    this.bCortexDisabledPanel = this.bCortexDialog.getByText("b-Cortex not enabled for your tenant", { exact: true }).first();

    // SnackbarComponent (re-exported as @ui/Toast, which this tab calls) mounts at the app root,
    // so it is a sibling of the dialog portals and MUI marks it aria-hidden while one is open.
    // That drops it out of the accessibility tree, so getByRole finds nothing even though the
    // node is in the DOM — matched by attribute instead, the same form the sibling suites use.
    this.notificationsRegion = page.locator('[aria-label="Notifications"]');
  }

  // The four role-and-name lookups the row controls and their confirm dialogs share. Built
  // through one accessor each rather than written out per scope: a row's control and the dialog
  // control it opens carry the same accessible name, and only the scope separates them.
  keepButtonsIn(scope: Locator): Locator {
    return scope.getByRole("button", { name: "Keep", exact: true });
  }

  unkeepButtonsIn(scope: Locator): Locator {
    return scope.getByRole("button", { name: "Unkeep", exact: true });
  }

  discardButtonsIn(scope: Locator): Locator {
    return scope.getByRole("button", { name: "Discard", exact: true });
  }

  cancelButtonIn(scope: Locator): Locator {
    return scope.getByRole("button", { name: "Cancel", exact: true });
  }

  // ds/Chip renders a plain span for a non-interactive tag, so this is a scoped text match by
  // necessity. Exact, so it cannot also match the header description, which carries the word
  // inside a longer sentence.
  keptChipIn(scope: Locator): Locator {
    return scope.getByText("Kept", { exact: true });
  }

  // Toast text, kept inside the notifications region so a matching string elsewhere on the page
  // cannot pass for a toast. The fallback matches the toast roles by attribute, for the same
  // aria-hidden reason the region itself is matched that way.
  toastWithText(text: string): Locator {
    return this.notificationsRegion
      .getByText(text)
      .or(this.page.locator('[role="status"], [role="alert"]').filter({ hasText: text }))
      .first();
  }

  // One ds/Modal, matched by its own title. Matched by attribute rather than getByRole because MUI
  // aria-hides a dialog the moment another stacks over it, and a role lookup would then read "the
  // modal closed" while it is still perfectly on screen — the trap nubiLocators.ts and
  // collectiveLocators.ts both document. Every dialog on this surface is reached through here, so
  // the form is stated once instead of per dialog.
  dialogTitled(title: string): Locator {
    return this.page.locator('[role="dialog"]').filter({ hasText: title }).first();
  }

  // A row carries no handle of its own, so it is reached from one of its own cells. Its direct
  // children are the content box and the action cluster, so any cell's second ancestor div is the
  // row — true of an action button and of the value line alike. Walking the ancestor axis rather
  // than across preceding siblings keeps this correct however the Kept chip, the module chip and
  // the optional View link sit between the two.
  private rowFromCell(cell: Locator): Locator {
    return cell.locator("xpath=ancestor::div[2]");
  }

  // The text lines of a row: Typography renders <p>, so a row holds its value and then, where it
  // has one, its key. One accessor because the two callers below need the same lookup at different
  // scopes — down from a row already in hand, and across the dialog to find a row by its text.
  private valueLinesIn(scope: Locator): Locator {
    return scope.locator("p");
  }

  // The value line of a row, which is how that row is identified again after a refetch.
  valueOf(actionButton: Locator): Locator {
    return this.valueLinesIn(this.rowFromCell(actionButton)).first();
  }

  // The row whose value line reads exactly `value`. Anchored regex rather than hasText's substring
  // match: two inferred preferences can legitimately share a prefix, and a substring match would
  // then point a case at the wrong row and still pass.
  rowWithValue(value: string): Locator {
    return this.rowFromCell(this.valueLinesIn(this.bCortexDialog).filter({ hasText: exactText(value) }).first());
  }
}

// Anchored, literal-safe regex for one row's value. Inferred preference values are backend prose
// and routinely carry ( ) . * — characters a bare string would hand to the regex engine as syntax.
// The \s* on both ends is load-bearing: hasText does NOT normalise whitespace for a RegExp (checked
// against this Playwright build — a `<p>` reading "  value \n " matches `^\s*value\s*$` and does not
// match `^value$`), while readRowValue trims what it reads. Without them a stored value carrying
// any padding would be looked up by its trimmed form and never found again after a refetch.
function exactText(value: string): RegExp {
  return new RegExp(`^\\s*${value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*$`);
}
