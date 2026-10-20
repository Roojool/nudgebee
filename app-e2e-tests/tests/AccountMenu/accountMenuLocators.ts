import { Page, Locator } from "@playwright/test";
import { CommonLocators } from "../GlobalLocators";

// The user/account menu (app/src/components/common/layout/index.jsx) opens on
// #account-setting; the MUI Menu id="menu-appbar" holds its items, including the
// disabled "Version: <version>" row rendered by UserMenuItems.jsx (no testid there).
export class AccountMenuLocators extends CommonLocators {
  readonly accountSettingBtn: Locator;
  readonly userMenu: Locator;
  readonly versionMenuItem: Locator;

  constructor(page: Page) {
    super(page);

    // No data-testid and no aria-label on this icon-only IconButton (grep -c
    // data-testid on index.jsx returns 1, unrelated to this button) — id is the
    // only stable contract, so it stands alone with no .or() fallback.
    this.accountSettingBtn = page.locator("#account-setting");
    // menu-appbar is the only MUI Menu id in this component and is hardcoded in
    // JSX (not testid-backed); a role="menu" fallback would be ambiguous against
    // the other kebab-menus rendered elsewhere in the app.
    this.userMenu = page.locator("#menu-appbar");
    this.versionMenuItem = this.userMenu.getByText(/^Version:/);
  }
}
