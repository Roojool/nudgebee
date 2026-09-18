#!/usr/bin/env node
/**
 * Design-system import gate.
 *
 * Fails when a change ADDS an import that `app/design-system.md` says has a
 * design-system replacement — a raw MUI component with a `ds/*` primitive, or
 * a legacy component a DS V2 superseded. Existing imports are never flagged:
 * each changed file is compared with its own version at the merge base, so
 * ~300 files of pre-existing drift don't block anyone, but a file that has
 * been cleaned up cannot quietly regress (optimise-new/DetailsPanel went back
 * to MUI Divider two days after it was migrated).
 *
 * Scope is import-level drift only. Hardcoded colours / spacing / fonts are
 * not checked here — a regex over `sx` values is too noisy to block on.
 *
 * Why diffing against the base rather than a committed baseline file: a
 * baseline every cleanup PR must edit becomes a merge-conflict hotspot across
 * main / test / prod, and a conflicted PR runs no CI at all.
 *
 * Base resolution:
 *   - CI (`GITHUB_BASE_REF` set): only PRs into `main` are checked, against
 *     `origin/main`. Promotion and hotfix PRs carry already-reviewed code.
 *   - Local: `DS_IMPORTS_BASE`, else `enterprise/main`, else `origin/main`.
 *     Uncommitted and untracked files are included. Skips (exit 0) when no
 *     base resolves or the base is implausibly far behind.
 *
 * Escape hatch: put `// ds-imports-allow: <reason>` on the line above the
 * import (or at the end of it) when the DS component genuinely cannot do what
 * the code needs. A marker covers one restricted import: when a line names two
 * (`import { Dialog, Menu } …`), split them so each is justified on its own.
 */

const { execFileSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const ts = require('typescript');

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
const ALLOW_MARKER = /ds-imports-allow:\s*\S/;
const ALIASES = { '@ui/': 'components/common/ds/', '@shared/': 'components/common/', '@components/': 'components/', 'src/': '' };
const MAX_BASE_DISTANCE = 1000;

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

  // An allow marker (end of the line, or a comment-only line above) covers a line only when that line
  // holds exactly one restricted import — so a justified Dialog cannot carry an unjustified Menu.
  const perLine = new Map();
  for (const h of restricted) perLine.set(h.line, (perLine.get(h.line) || 0) + 1);
  const commentOnly = /^\s*(\/\/|\/\*)/;
  const allowed = (h) =>
    perLine.get(h.line) === 1 &&
    (ALLOW_MARKER.test(lines[h.line]) || (h.line > 0 && commentOnly.test(lines[h.line - 1]) && ALLOW_MARKER.test(lines[h.line - 1])));
  return restricted.filter((h) => !allowed(h)).map((h) => ({ ...h, line: h.line + 1 }));
}

/** Hits whose key does not appear in the base version of the file. */
function newRestrictedImports(baseSource, headSource, file, baseFile = file) {
  const baseKeys = new Set(baseSource == null ? [] : findRestrictedImports(baseSource, baseFile).map((h) => h.key));
  return findRestrictedImports(headSource, file).filter((h) => !baseKeys.has(h.key));
}

function git(args, cwd) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, stdio: ['ignore', 'pipe', 'ignore'] });
}

function resolveBase(cwd) {
  const baseRef = process.env.GITHUB_BASE_REF;
  if (baseRef && baseRef !== 'main') {
    return { skip: `PR targets '${baseRef}', not main — promotion/hotfix PRs are not checked.` };
  }
  const candidates = baseRef ? [`origin/${baseRef}`] : [process.env.DS_IMPORTS_BASE, 'enterprise/main', 'origin/main'].filter(Boolean);
  for (const ref of candidates) {
    let sha;
    try {
      sha = git(['merge-base', ref, 'HEAD'], cwd).trim();
    } catch {
      continue;
    }
    const distance = Number(git(['rev-list', '--count', `${sha}..HEAD`], cwd).trim());
    // Guards the implicit fallbacks only — an explicit CI or DS_IMPORTS_BASE ref is trusted.
    if (!baseRef && ref !== process.env.DS_IMPORTS_BASE && distance > MAX_BASE_DISTANCE) {
      return { skip: `${ref} is ${distance} commits behind HEAD — fetch it or set DS_IMPORTS_BASE=<ref>.` };
    }
    return { ref, sha };
  }
  return { skip: `no base ref found (tried ${candidates.join(', ')}) — set DS_IMPORTS_BASE=<ref>.` };
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
    console.log(`check-ds-imports: skipped — ${base.skip}`);
    return;
  }

  const violations = [];
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
    for (const hit of newRestrictedImports(baseSource, headSource, file, baseFile || file)) {
      violations.push({ file, ...hit });
    }
  }

  if (violations.length > 0) {
    console.error(`check-ds-imports: ${violations.length} new import(s) with a design-system replacement (base ${base.ref}):\n`);
    for (const v of violations) {
      const what = v.key.startsWith('legacy:') ? `legacy ${v.key.slice(7)}` : `MUI ${v.key.split(':')[1]}`;
      console.error(`  ${v.file}:${v.line}  ${what}  →  use ${v.replacement}`);
    }
    console.error('\nSee app/design-system.md §3. If the DS component genuinely cannot do what this code');
    console.error('needs, add `// ds-imports-allow: <reason>` above the import and say why in the PR.');
    process.exit(1);
  }
  console.log(`check-ds-imports: no new design-system import drift (base ${base.ref}).`);
}

module.exports = { findRestrictedImports, newRestrictedImports, resolveModule, isChecked };

if (require.main === module) {
  main();
}
