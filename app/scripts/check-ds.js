#!/usr/bin/env node
/**
 * Design-system gate.
 *
 * Fails when a change ADDS drift away from `app/design-system.md`, in two classes:
 *
 *   - **Imports** (§3) — a raw MUI component that has a `ds/*` primitive, or a
 *     legacy component a DS V2 superseded. Also the one Edit and one Delete
 *     icon the app standardised on (`@assets` + `SafeIcon`).
 *   - **Tokens** (§1.8) — a hardcoded colour, spacing, radius, font size or
 *     weight that has an exact `--ds-*` equivalent. Rules live in
 *     `ds-tokens.js`; the token table is read from `theme-tokens.css` so it
 *     cannot drift from the doc.
 *
 * Existing drift is never flagged: each changed file is compared with its own
 * version at the merge base, so the ~300 files of pre-existing drift don't
 * block anyone, but a file that has been cleaned up cannot quietly regress
 * (optimise-new/DetailsPanel went back to MUI Divider two days after it was
 * migrated). Imports compare as a set, token values as a multiset — three
 * `16px` paddings becoming four reports one violation, and moving a line
 * reports none.
 *
 * Why diffing against the base rather than a committed baseline file: a
 * baseline every cleanup PR must edit becomes a merge-conflict hotspot across
 * main / test / prod, and a conflicted PR runs no CI at all.
 *
 * Base resolution:
 *   - CI (`GITHUB_BASE_REF` set): only PRs into `main` are checked, against
 *     `origin/main`. Promotion and hotfix PRs carry already-reviewed code.
 *   - Local: `DS_BASE` (or legacy `DS_IMPORTS_BASE`), else `enterprise/main`,
 *     else `origin/main`. Uncommitted and untracked files are included. Skips
 *     (exit 0) when no base resolves or the base is implausibly far behind.
 *
 * `--file <path>`: check one file against its committed version instead of
 * walking the diff. Used by the `PostToolUse` hook in `.claude/settings.json`
 * so an agent is told about drift it just wrote, while it still has the
 * context to fix it. Prints nothing and exits 0 when the file is clean.
 *
 * Escape hatch: put `// ds-allow: <reason>` on the line above the offending
 * line (or at the end of it) when the design system genuinely cannot express
 * what the code needs. A marker covers one restricted import, so a line naming
 * two (`import { Dialog, Menu } …`) must be split; for token values it covers
 * the whole line, since a raw value is justified by the line it sits on.
 * `// ds-imports-allow:` is still accepted as a spelling of the same marker.
 */

const { execFileSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const ts = require('typescript');
const { findTokenViolations, loadTokens } = require('./ds-tokens');

// MUI export → replacement, only where design-system.md §1/§3 names one
// unambiguously. Deliberately absent: Box/Stack/Grid/Typography (layout, used
// by the doc's own examples), CircularProgress, Collapse, Popover/Popper,
// Radio, List (#37517 was closed as the wrong primitive), Tabs (no ds/Tabs
// file), Autocomplete (no ds/Autocomplete), Paper (6 of its 7 uses are floating
// surfaces — the drawer panel, suggestion dropdowns, Autocomplete's paper slot —
// which ds/Card, a static page container, cannot serve).
const MUI_REPLACEMENTS = {
  Accordion: '@ui/Accordion',
  AccordionDetails: '@ui/Accordion',
  AccordionSummary: '@ui/Accordion',
  Alert: '@ui/Banner (page/section notice) or @ui/Callout (inline hint under a field)',
  Button: '@ui/Button',
  Card: '@ui/Card',
  CardContent: '@ui/Card',
  Checkbox: '@ui/Checkbox',
  Chip: '@ui/Chip (interactive) or @ui/Label (read-only status)',
  Dialog: '@ui/Modal',
  DialogActions: '@ui/Modal',
  DialogContent: '@ui/Modal',
  DialogTitle: '@ui/Modal',
  Divider: '@ui/Divider',
  IconButton: "@ui/Button with composition='icon-only'",
  LinearProgress: '@ui/ProgressBar (known max) or @ui/ProgressLinear',
  Link: '@ui/Link',
  Menu: '@ui/DropdownMenu',
  MenuItem: '@ui/DropdownMenu',
  Select: '@ui/Select',
  Skeleton: '@ui/Skeleton',
  Step: '@ui/Stepper',
  StepLabel: '@ui/Stepper',
  Stepper: '@ui/Stepper',
  Switch: '@ui/Switch',
  Table: '@shared/tables/CustomTable',
  TableBody: '@shared/tables/CustomTable',
  TableCell: '@shared/tables/CustomTable',
  TableContainer: '@shared/tables/CustomTable',
  TableHead: '@shared/tables/CustomTable',
  TableRow: '@shared/tables/CustomTable',
  TextField: '@ui/Input',
  ToggleButton: '@ui/Toggle or @ui/ToggleGroup',
  ToggleButtonGroup: '@ui/Toggle or @ui/ToggleGroup',
  Tooltip: '@ui/Tooltip',
};

// Legacy module (path under src/, no extension) → replacement.
const LEGACY_REPLACEMENTS = {
  'components/common/forms/CustomTextField': '@ui/Input',
  'components/common/navigation/CustomStepper': '@ui/Stepper',
  'components/common/navigation/NewVerticalStepper': '@ui/Stepper',
  'components/common/navigation/VerticalStepNavigation': '@ui/Stepper',
};

// Edit / Delete action icons. The app standardised on one of each, both in
// `@assets` and rendered through `SafeIcon` (#16046 → #28615 → #38774 — three
// passes over the same drift, so the fourth is blocked here instead).
const EDIT_ICON = '@assets EditIcon via <SafeIcon src={EditIcon} />';
const DELETE_ICON = '@assets DeleteIconRed via <SafeIcon src={DeleteIconRed} />';
// Base names × MUI's theme suffixes (DeleteRounded, EditTwoTone, ModeEditOutlineSharp, …).
// Deliberately NOT here: DeleteSweep, EditCalendar, EditLocation, EditNote…s, EditOff, EditRoad,
// EditAttributes — different icons with their own meanings, not this app's edit/delete action.
const MUI_ICON_BASES = {
  Delete: DELETE_ICON,
  DeleteForever: DELETE_ICON,
  DeleteOutline: DELETE_ICON,
  Edit: EDIT_ICON,
  EditNote: EDIT_ICON,
  ModeEdit: EDIT_ICON,
  ModeEditOutline: EDIT_ICON,
  DriveFileRenameOutline: EDIT_ICON,
};
const MUI_ICON_SUFFIXES = ['', 'Outlined', 'Rounded', 'Sharp', 'TwoTone'];
const MUI_ICON_REPLACEMENTS = Object.fromEntries(
  Object.entries(MUI_ICON_BASES).flatMap(([base, replacement]) => MUI_ICON_SUFFIXES.map((suffix) => [base + suffix, replacement]))
);

// Superseded `@assets` exports. `writeIconLight` is the same drawing in a lighter
// gray; it survives only on filled surfaces where the standard icon has no contrast.
const LEGACY_ICON_EXPORTS = { writeIcon: EDIT_ICON, writeIconLight: EDIT_ICON, EditNewIcon: EDIT_ICON };

// react-icons ships a dozen edit/trash glyphs (LiaEditSolid, FiTrash2, …); match by name.
const REACT_ICON_NAME = /(Edit|Pencil|Trash|Delete)/;

// The primitives themselves, and the compositions design-system.md sanctions
// that are built on the very MUI components they stand in for.
const EXEMPT_PREFIXES = [
  'src/components/common/ds/',
  'src/components/common/tables/',
  'src/components/common/CustomDropdown.',
  'src/components/common/widgets/CustomDateTimePicker.',
  'src/components/common/navigation/Tabs.',
];

const SOURCE_FILE = /\.(tsx?|jsx?)$/;
const NON_SOURCE = /(\.d\.ts|\.(test|spec|stories)\.[tj]sx?)$|\/__tests__\//;
const ALLOW_MARKER = /ds-(?:imports-)?allow:\s*\S/;
const ALIASES = { '@ui/': 'components/common/ds/', '@shared/': 'components/common/', '@components/': 'components/', 'src/': '' };
const MAX_BASE_DISTANCE = 1000;
const TOKENS_CSS = 'src/styles/theme-tokens.css';
const TOKENS_TS = 'src/utils/colors.ts';

/** True when `file` (app-relative, e.g. `src/pages/x.tsx`) is in scope. */
function isChecked(file) {
  return file.startsWith('src/') && SOURCE_FILE.test(file) && !NON_SOURCE.test(file) && !EXEMPT_PREFIXES.some((p) => file.startsWith(p));
}

/** Resolve an import specifier to a src-relative module path without extension, or null. */
function resolveModule(specifier, file) {
  let resolved = null;
  const alias = Object.keys(ALIASES).find((a) => specifier.startsWith(a));
  if (alias) {
    resolved = ALIASES[alias] + specifier.slice(alias.length);
  } else if (specifier.startsWith('.')) {
    resolved = path.posix.relative('src', path.posix.join(path.posix.dirname(file), specifier));
  }
  return resolved ? resolved.replace(/\.(tsx?|jsx?)$/, '').replace(/\/index$/, '') : null;
}

/**
 * Restricted imports in one file's source.
 * @returns {{ key: string, line: number, replacement: string }[]}
 */
function findRestrictedImports(source, file) {
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, file.endsWith('.ts') ? ts.ScriptKind.TS : ts.ScriptKind.TSX);
  const lines = source.split('\n');
  const hits = [];
  let importsAutocomplete = false;

  // `node` is the import specifier itself for named imports, so each hit carries its own line.
  const add = (node, key, replacement) => {
    hits.push({ key, line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line, replacement });
  };

  const checkSpecifier = (node, specifier, names) => {
    if (specifier === '@mui/material') {
      for (const { name, element } of names) {
        if (name === 'Autocomplete') importsAutocomplete = true;
        if (MUI_REPLACEMENTS[name]) add(element, `@mui/material:${name}`, MUI_REPLACEMENTS[name]);
      }
      return;
    }
    const sub = specifier.match(/^@mui\/material\/([A-Za-z]+)$/);
    if (sub) {
      if (sub[1] === 'Autocomplete') importsAutocomplete = true;
      if (MUI_REPLACEMENTS[sub[1]]) add(node, `@mui/material:${sub[1]}`, MUI_REPLACEMENTS[sub[1]]);
      return;
    }
    if (specifier === '@mui/icons-material') {
      for (const { name, element } of names) {
        if (MUI_ICON_REPLACEMENTS[name]) add(element, `@mui/icons-material:${name}`, MUI_ICON_REPLACEMENTS[name]);
      }
      return;
    }
    const iconSub = specifier.match(/^@mui\/icons-material\/([A-Za-z]+)$/);
    if (iconSub) {
      if (MUI_ICON_REPLACEMENTS[iconSub[1]]) add(node, `@mui/icons-material:${iconSub[1]}`, MUI_ICON_REPLACEMENTS[iconSub[1]]);
      return;
    }
    if (specifier === '@assets') {
      for (const { name, element } of names) {
        if (LEGACY_ICON_EXPORTS[name]) add(element, `@assets:${name}`, LEGACY_ICON_EXPORTS[name]);
      }
      return;
    }
    if (specifier.startsWith('react-icons/')) {
      for (const { name, element } of names) {
        if (REACT_ICON_NAME.test(name))
          add(element, `react-icons:${name}`, name.includes('Trash') || name.includes('Delete') ? DELETE_ICON : EDIT_ICON);
      }
      return;
    }
    const mod = resolveModule(specifier, file);
    if (mod && LEGACY_REPLACEMENTS[mod]) add(node, `legacy:${path.posix.basename(mod)}`, LEGACY_REPLACEMENTS[mod]);
  };

  const visit = (node) => {
    if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier) && !node.importClause?.isTypeOnly) {
      const bindings = node.importClause?.namedBindings;
      const names =
        bindings && ts.isNamedImports(bindings)
          ? bindings.elements.filter((e) => !e.isTypeOnly).map((e) => ({ name: (e.propertyName || e.name).text, element: e }))
          : [];
      checkSpecifier(node, node.moduleSpecifier.text, names);
    } else if (ts.isExportDeclaration(node) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier) && !node.isTypeOnly) {
      const names =
        node.exportClause && ts.isNamedExports(node.exportClause)
          ? node.exportClause.elements.map((e) => ({ name: (e.propertyName || e.name).text, element: e }))
          : [];
      checkSpecifier(node, node.moduleSpecifier.text, names);
    } else if (
      ts.isCallExpression(node) &&
      node.expression.kind === ts.SyntaxKind.ImportKeyword &&
      node.arguments[0] &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      checkSpecifier(node, node.arguments[0].text, []);
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);

  // MUI Autocomplete (no DS equivalent) needs MUI TextField for renderInput.
  const restricted = importsAutocomplete ? hits.filter((h) => h.key !== '@mui/material:TextField') : hits;

  return applyAllowMarkers(restricted, lines, { oneHitPerLine: true });
}

/**
 * Drop hits whose line carries an allow marker, at the end of the line or on a
 * comment-only line directly above. Returns 1-based lines for reporting.
 *
 * `oneHitPerLine` is the import rule: a marker covers a line only when that
 * line holds exactly one restricted import, so a justified `Dialog` cannot
 * smuggle in an unjustified `Menu`. Token values don't need it — `padding` and
 * `color` on one line are one decision, justified together.
 */
function applyAllowMarkers(hits, lines, { oneHitPerLine }) {
  const perLine = new Map();
  for (const h of hits) perLine.set(h.line, (perLine.get(h.line) || 0) + 1);
  const commentOnly = /^\s*(\/\/|\/\*)/;
  const allowed = (h) =>
    (!oneHitPerLine || perLine.get(h.line) === 1) &&
    (ALLOW_MARKER.test(lines[h.line] || '') ||
      (h.line > 0 && commentOnly.test(lines[h.line - 1] || '') && ALLOW_MARKER.test(lines[h.line - 1] || '')));
  return hits.filter((h) => !allowed(h)).map((h) => ({ ...h, line: h.line + 1 }));
}

/** Hits whose key does not appear in the base version of the file. */
function newRestrictedImports(baseSource, headSource, file, baseFile = file) {
  const baseKeys = new Set(baseSource == null ? [] : findRestrictedImports(baseSource, baseFile).map((h) => h.key));
  return findRestrictedImports(headSource, file).filter((h) => !baseKeys.has(h.key));
}

/**
 * Token hits beyond what the base version of the file already had.
 *
 * A multiset, not a set: the same `16px` can legitimately appear many times, so
 * going from three to four reports one violation rather than four or none. The
 * reported hits are the last ones in file order — arbitrary but stable, and the
 * only honest answer when the value itself carries no identity.
 */
function newTokenViolations(baseSource, headSource, file, tokens, baseFile = file) {
  const budget = new Map();
  if (baseSource != null) {
    for (const h of findTokenViolations(baseSource, baseFile, tokens)) budget.set(h.key, (budget.get(h.key) || 0) + 1);
  }
  const head = findTokenViolations(headSource, file, tokens);
  const lines = headSource.split('\n');
  const kept = [];
  for (const h of head) {
    const left = budget.get(h.key) || 0;
    if (left > 0) budget.set(h.key, left - 1);
    else kept.push(h);
  }
  return applyAllowMarkers(kept, lines, { oneHitPerLine: false });
}

function git(args, cwd) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, stdio: ['ignore', 'pipe', 'ignore'] });
}

function resolveBase(cwd) {
  const baseRef = process.env.GITHUB_BASE_REF;
  if (baseRef && baseRef !== 'main') {
    return { skip: `PR targets '${baseRef}', not main — promotion/hotfix PRs are not checked.` };
  }
  const explicit = process.env.DS_BASE || process.env.DS_IMPORTS_BASE;
  const candidates = baseRef ? [`origin/${baseRef}`] : [explicit, 'enterprise/main', 'origin/main'].filter(Boolean);
  for (const ref of candidates) {
    let sha;
    try {
      sha = git(['merge-base', ref, 'HEAD'], cwd).trim();
    } catch {
      continue;
    }
    // An explicit CI or DS_BASE ref is trusted, so its distance is never needed.
    if (baseRef || ref === explicit) return { ref, sha };

    // In a shallow clone `merge-base` can succeed and this still die
    // ("fatal: unknown revision" once the traversal hits the graft boundary),
    // so an unguarded call would end `lint2` in a stack trace. Not measurable
    // means the staleness guard below cannot run, and the guard is the only
    // thing stopping a stale implicit ref from producing a diff of hundreds of
    // files — so skip and say how to fix it rather than trusting the ref.
    let distance;
    try {
      distance = Number(git(['rev-list', '--count', `${sha}..HEAD`], cwd).trim());
    } catch {
      return { skip: `cannot measure the distance to ${ref} (shallow clone?) — set DS_BASE=<ref>.` };
    }
    if (distance > MAX_BASE_DISTANCE) {
      return { skip: `${ref} is ${distance} commits behind HEAD — fetch it or set DS_BASE=<ref>.` };
    }
    return { ref, sha };
  }
  return { skip: `no base ref found (tried ${candidates.join(', ')}) — set DS_BASE=<ref>.` };
}

/** Changed files as [{ file, baseFile|null }], app-relative, against the working tree. */
function changedFiles(sha, cwd) {
  const out = git(['diff', '--name-status', '-M', '-z', '--relative', sha, '--', 'src'], cwd).split('\0');
  const changes = [];
  for (let i = 0; i < out.length - 1; ) {
    const status = out[i++];
    if (status.startsWith('R') || status.startsWith('C')) {
      const baseFile = out[i++];
      changes.push({ file: out[i++], baseFile: status.startsWith('R') ? baseFile : null });
    } else if (status === 'D') {
      i++;
    } else {
      const file = out[i++];
      changes.push({ file, baseFile: status === 'A' ? null : file });
    }
  }
  for (const file of git(['ls-files', '--others', '--exclude-standard', '-z', '--', 'src'], cwd).split('\0').filter(Boolean)) {
    changes.push({ file, baseFile: null });
  }
  return changes;
}

function main() {
  const appDir = path.resolve(__dirname, '..');
  const base = resolveBase(appDir);
  if (base.skip) {
    console.log(`check-ds: skipped — ${base.skip}`);
    return;
  }
  const tokens = loadTokens(path.join(appDir, TOKENS_CSS), path.join(appDir, TOKENS_TS));

  const imports = [];
  const values = [];
  for (const { file, baseFile } of changedFiles(base.sha, appDir)) {
    if (!isChecked(file)) continue;
    const headSource = fs.readFileSync(path.join(appDir, file), 'utf8');
    let baseSource = null;
    if (baseFile) {
      try {
        baseSource = git(['show', `${base.sha}:./${baseFile}`], appDir);
      } catch {
        baseSource = null;
      }
    }
    for (const hit of newRestrictedImports(baseSource, headSource, file, baseFile || file)) imports.push({ file, ...hit });
    for (const hit of newTokenViolations(baseSource, headSource, file, tokens, baseFile || file)) values.push({ file, ...hit });
  }

  if (imports.length + values.length === 0) {
    console.log(`check-ds: no new design-system drift (base ${base.ref}).`);
    return;
  }
  report(imports, values, `base ${base.ref}`);
  process.exit(1);
}

/** Print both classes of violation in the same shape, whatever found them. */
function report(imports, values, scope) {
  const total = imports.length + values.length;
  console.error(`check-ds: ${total} new design-system violation(s) (${scope}):\n`);
  for (const v of imports) {
    const what = v.key.startsWith('legacy:') ? `legacy ${v.key.slice(7)}` : `MUI ${v.key.split(':')[1]}`;
    console.error(`  ${v.file}:${v.line}  ${what}  →  use ${v.replacement}`);
  }
  for (const v of values) {
    console.error(`  ${v.file}:${v.line}  hardcoded ${v.value}  →  use ${v.token}  (${v.rule})`);
  }
  if (imports.length > 0) console.error('\nSee app/design-system.md §3 for which primitive replaces which MUI component.');
  if (values.length > 0) console.error(`\nSee app/design-system.md §1.8 for the token scales. Import with \`import { ds } from '@utils/colors';\`.`);
  console.error('If the design system genuinely cannot express what this code needs, add');
  console.error('`// ds-allow: <reason>` above the line and say why in the PR.');
}

/**
 * Drift one file has gained over its committed version.
 *
 * Against HEAD, not the merge base: the question this answers is "did the edit
 * that just happened add drift", so anything already committed is off limits.
 * That is what stops a one-line change to a file with 200 pre-existing values
 * from reporting 200 violations the author did not write.
 *
 * Returns `null` when the file is out of scope, so callers can stay silent.
 */
function violationsForFile(target) {
  const appDir = path.resolve(__dirname, '..');
  // Forward slashes even on Windows: `isChecked` tests `src/` and the exempt
  // prefixes, so a backslash path would silently match nothing and the hook
  // would go quiet instead of reporting.
  const file = path.relative(appDir, path.resolve(target)).split(path.sep).join('/');
  if (file.startsWith('..') || !isChecked(file) || !fs.existsSync(path.join(appDir, file))) return null;

  const tokens = loadTokens(path.join(appDir, TOKENS_CSS), path.join(appDir, TOKENS_TS));
  const headSource = fs.readFileSync(path.join(appDir, file), 'utf8');
  let baseSource = null;
  try {
    baseSource = git(['show', `HEAD:./${file}`], appDir);
  } catch {
    baseSource = null; // new file — everything in it is new
  }
  return {
    file,
    imports: newRestrictedImports(baseSource, headSource, file).map((h) => ({ file, ...h })),
    values: newTokenViolations(baseSource, headSource, file, tokens).map((h) => ({ file, ...h })),
  };
}

/**
 * `--file <path>` — silent and exit 0 when clean, so a check that runs on every
 * frontend edit only speaks up when it has something to say. Never fails the
 * edit: this reports, `lint2` is what blocks.
 */
function checkOneFile(target) {
  const found = violationsForFile(target);
  if (found && found.imports.length + found.values.length > 0) {
    report(found.imports, found.values, 'uncommitted edits to this file');
  }
}

module.exports = { findRestrictedImports, newRestrictedImports, newTokenViolations, violationsForFile, resolveModule, isChecked };

if (require.main === module) {
  const fileFlag = process.argv.indexOf('--file');
  const jsonFlag = process.argv.indexOf('--json');
  if (jsonFlag !== -1 && process.argv[jsonFlag + 1]) {
    // Machine-readable single-file mode for `.claude/hooks/ds-guard.js`.
    process.stdout.write(JSON.stringify(violationsForFile(process.argv[jsonFlag + 1])));
  } else if (fileFlag !== -1 && process.argv[fileFlag + 1]) {
    checkOneFile(process.argv[fileFlag + 1]);
  } else {
    main();
  }
}
