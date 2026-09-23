#!/usr/bin/env node
/**
 * Token rules for the design-system gate (`check-ds.js`).
 *
 * `app/design-system.md` §1.8 says: never hardcode a colour, spacing, radius,
 * font size or weight that has a `--ds-*` token. This module finds the places
 * a changed file broke that rule.
 *
 * Two decisions shape everything below:
 *
 * 1. **The token table is read from `src/styles/theme-tokens.css` at runtime**,
 *    never copied here. §1.8's table and this gate cannot drift apart, and a
 *    new token step starts being enforced the moment it is defined.
 *
 * 2. **Values are matched exactly, never approximately.** `#6f6f6f` is
 *    `ds.gray[600]`; `#6f6f70` is a colour someone chose on purpose and is not
 *    flagged. An off-scale `13px` padding is left alone. The gate's whole
 *    value is that a hit is never arguable — a fuzzy match would make it
 *    arguable, and an arguable gate gets switched off.
 *
 * Detection is AST-based. `check-ds-imports`' original header called a regex
 * over `sx` "too noisy to block on" and it was right: a regex cannot tell
 * `padding: '16px'` in an `sx` from the same text in a string literal, a test
 * fixture, or a comment. We only look at property assignments that are
 * genuinely inside a style object, and at JSX attributes that take a CSS value.
 *
 * Known blind spot: Emotion / styled-components tagged templates
 * (``styled.div`padding: 16px;` ``) are CSS as text, not an object literal, so
 * nothing here sees them. `app/CLAUDE.md` lists that as a sanctioned styling
 * option, so this is a real gap rather than a hypothetical — closing it needs a
 * CSS parser over the template body, not another AST case. Stated plainly for
 * the same reason the chart-file exemption is: a gate whose coverage is guessed
 * at gets trusted where it shouldn't be.
 */

const fs = require('fs');
const path = require('path');
const ts = require('typescript');

// ── Token table ────────────────────────────────────────────────────────────

/**
 * Map every `--ds-*` custom property to the `ds` accessor that returns it, read
 * out of `src/utils/colors.ts` itself.
 *
 * Deriving the accessor from the variable name instead looks obvious and is a
 * trap: it invents accessors for tokens the `ds` object does not export. CSS
 * defines `--ds-slate-600`, `ds` has no `slate` key, and a gate that answered
 * "use `ds.slate[600]`" would hand someone a `TypeError` at render — a fix that
 * breaks the page is worse than the hardcoded colour it replaced. Reading the
 * real object means a suggestion cannot name something that does not exist, and
 * a token gains enforcement exactly when someone adds its accessor.
 *
 * @returns {Map<string, string>} `'--ds-gray-600'` → `'ds.gray[600]'`
 */
function loadAccessors(colorsPath) {
  const source = fs.readFileSync(colorsPath, 'utf8');
  const sf = ts.createSourceFile(colorsPath, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const accessors = new Map();

  const walk = (node, pathSoFar) => {
    if (!ts.isObjectLiteralExpression(node)) return;
    for (const prop of node.properties) {
      if (!ts.isPropertyAssignment(prop)) continue;
      const key = ts.isIdentifier(prop.name) || ts.isStringLiteral(prop.name) || ts.isNumericLiteral(prop.name) ? prop.name.text : null;
      if (key == null) continue;
      // Numeric steps read as `ds.gray[600]`, names as `ds.gray.alpha`.
      const next = /^\d+$/.test(key) ? `${pathSoFar}[${key}]` : `${pathSoFar}.${key}`;
      const value = prop.initializer;
      if (ts.isObjectLiteralExpression(value)) {
        walk(value, next);
      } else if (ts.isStringLiteral(value) || ts.isNoSubstitutionTemplateLiteral(value)) {
        const cssVar = value.text.match(/^var\((--ds-[a-z0-9-]+)\)$/);
        // First accessor wins: `ds.brand.heading` aliases `--ds-brand-heading`,
        // but the direct name is the one worth suggesting.
        if (cssVar && !accessors.has(cssVar[1])) accessors.set(cssVar[1], next);
      }
    }
  };

  const visit = (node) => {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text === 'ds' && node.initializer) {
      // `export const ds = {...} as const;`
      const init = ts.isAsExpression(node.initializer) ? node.initializer.expression : node.initializer;
      walk(init, 'ds');
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return accessors;
}

/** `#ABC` → `#aabbcc`; `rgba( 0 , 0,0 , .5 )` → `rgba(0,0,0,0.5)`. */
function normalizeColor(raw) {
  const value = raw.trim().toLowerCase();
  const hex = value.match(/^#([0-9a-f]{3,8})$/);
  if (hex) {
    const d = hex[1];
    const full = d.length === 3 || d.length === 4 ? [...d].map((c) => c + c).join('') : d;
    return `#${full.length === 8 && full.endsWith('ff') ? full.slice(0, 6) : full}`;
  }
  const fn = value.match(/^(rgba?|hsla?)\(([^)]*)\)$/);
  if (!fn) return null;
  const nums = fn[2]
    .split(/[\s,/]+/)
    .filter(Boolean)
    .map((n) => String(parseFloat(n)));
  // rgb(a) with a fully opaque alpha is the same colour as its 3-arg form.
  if (nums.length === 4 && nums[3] === '1') nums.pop();
  return `${fn[1].replace(/a$/, '')}${nums.length === 4 ? 'a' : ''}(${nums.join(',')})`;
}

/**
 * Parse `theme-tokens.css` into lookup tables. Later declarations win, matching
 * the CSS cascade — the file has two `:root` blocks.
 *
 * A token with no `ds` accessor is skipped rather than flagged: the gate can
 * only ask for a replacement it can name.
 */
function loadTokens(cssPath, colorsPath) {
  const css = fs.readFileSync(cssPath, 'utf8');
  const accessors = loadAccessors(colorsPath);
  const colors = new Map();
  const space = new Map();
  const radius = new Map();
  const fontSize = new Map();
  const fontWeight = new Map();

  for (const [, name, rawValue] of css.matchAll(/(--ds-[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    const value = rawValue.replace(/\/\*.*?\*\//g, '').trim();
    // A token defined as another token (`--ds-brand-heading: var(--ds-brand-500)`)
    // is an alias, not a value anyone could have hardcoded.
    if (value.startsWith('var(')) continue;
    const accessor = accessors.get(name);
    if (!accessor) continue;
    const px = value.match(/^(\d+(?:\.\d+)?)px$/);

    if (/^--ds-space-\d+$/.test(name) && px) space.set(Number(px[1]), accessor);
    else if (/^--ds-radius-/.test(name) && px) radius.set(Number(px[1]), accessor);
    else if (/^--ds-text-/.test(name) && px) fontSize.set(Number(px[1]), accessor);
    else if (/^--ds-font-weight-/.test(name) && /^\d+$/.test(value)) fontWeight.set(Number(value), accessor);
    else {
      const color = normalizeColor(value);
      // First definition wins for colours: `--ds-background-100` and
      // `--ds-gray-white` can share `#ffffff`, and the earlier name reads better.
      if (color && !colors.has(color)) colors.set(color, accessor);
    }
  }
  return { colors, space, radius, fontSize, fontWeight };
}

// ── What counts as a style value ───────────────────────────────────────────

// MUI's `sx` shorthands alongside their CSS names, grouped by the token scale
// that governs them. `theme.spacing` is MUI's default 8px, so `mt: 2` is 16px.
//
// `top`/`right`/`bottom`/`left`/`inset` are deliberately absent. MUI routes them
// through `positions.js`, which is a bare `style({ prop })` with no transform —
// `top: 2` renders `top: 2px`, not 16px (`gap` and the margin/padding family do
// go through `createUnaryUnit(theme, 'spacing', 8)`, which is why they are here).
// Reading a position as spacing would have told people to replace a 2px nudge
// with `ds.space[4]` and moved the element 14px. They are not layout spacing in
// §1.8's sense either, so the string form is left alone too.
const SPACING_PROPS = new Set([
  'padding',
  'paddingTop',
  'paddingBottom',
  'paddingLeft',
  'paddingRight',
  'paddingX',
  'paddingY',
  'margin',
  'marginTop',
  'marginBottom',
  'marginLeft',
  'marginRight',
  'marginX',
  'marginY',
  'gap',
  'rowGap',
  'columnGap',
  'p',
  'pt',
  'pb',
  'pl',
  'pr',
  'px',
  'py',
  'm',
  'mt',
  'mb',
  'ml',
  'mr',
  'mx',
  'my',
]);
const RADIUS_PROPS = new Set(['borderRadius']);
const FONT_SIZE_PROPS = new Set(['fontSize']);
const FONT_WEIGHT_PROPS = new Set(['fontWeight']);
const COLOR_PROPS = new Set([
  'color',
  'backgroundColor',
  'bgcolor',
  'background',
  'borderColor',
  'borderTopColor',
  'borderBottomColor',
  'borderLeftColor',
  'borderRightColor',
  'outlineColor',
  'fill',
  'stroke',
  'caretColor',
  'textDecorationColor',
  'columnRuleColor',
  'boxShadow',
  'textShadow',
  'border',
  'borderTop',
  'borderBottom',
  'borderLeft',
  'borderRight',
  'outline',
]);

// design-system.md's own Modal recipe sets `minWidth: '140px'` / `maxHeight:
// '600px'`: box dimensions have no token scale and never get flagged.
const DIMENSION_PROPS = /^(min|max)?(Width|Height|width|height)$/;

// MUI spacing unit → px. `theme.spacing(1)` is 8px by default and this app does
// not override it (`createDynamicTheme` sets palette and typography only).
const MUI_SPACING_UNIT = 8;

// Every style prop, as a bare JSX attribute. MUI's `extendSxProp`
// (`@mui/system/esm/styleFunctionSx/extendSxProp.js`) folds any attribute whose
// name is a `defaultSxConfig` key into `sx` and transforms it identically, so
// `<Box mt='24px'>` and `<Box sx={{ mt: '24px' }}>` render the same thing and
// must be judged the same way. An earlier allowlist of nine names let the whole
// bare-attribute idiom — which this codebase uses constantly — walk past.
const CSS_VALUE_ATTRS = new Set([...SPACING_PROPS, ...RADIUS_PROPS, ...FONT_SIZE_PROPS, ...FONT_WEIGHT_PROPS, ...COLOR_PROPS]);

// Icons are sized in px because the type scale does not describe them — §1.8's
// table has no icon step, and #38586 deliberately left `AccountEnvText`'s
// 16px icon alone. Any `fontSize`/dimension inside an icon element is skipped.
const ICON_TAG = /(^|[A-Z])(Icon|Svg|Logo)([A-Z]|$)|Icon$|Svg$/;

// Charts need more colour stops than the palette defines, and their geometry is
// pixel math rather than layout spacing. Excluded wholesale — a palette array
// of 12 hexes would otherwise be 12 unfixable violations.
const CHART_PATH = /(^|\/)(charts?|heatmaps?|graphs?|sparklines?)(\/|\.)|(Chart|Heatmap|Sparkline|Gauge|Treemap)\.[jt]sx?$/;

/** True when this file's values are exempt from the token rules. */
function isTokenExempt(file) {
  return CHART_PATH.test(file.split(path.sep).join('/'));
}

// ── AST walk ───────────────────────────────────────────────────────────────

// A declaration whose name says it holds styles: `panelSx`, `BODY_SX`,
// `headerStyles`, `getRowStyle`. Case-insensitive on purpose — the repo has both
// `const panelSx` and `const BODY_SX` (DigestsTab) / `DETAIL_GRID_SX` (ecs
// Instances), and a case-sensitive test silently skipped the shouted ones.
const STYLE_NAME = /(sx|styles?)$/i;

/**
 * True when `node` sits inside something that becomes CSS: an `sx`/`style` JSX
 * attribute, a declaration whose name says it holds styles, or the body of a
 * function that builds one. Without this test a `{ color: '#fff' }` entry in a
 * chart config or an API payload would be flagged.
 */
function isInsideStyleObject(node) {
  for (let n = node.parent; n; n = n.parent) {
    if (ts.isJsxAttribute(n)) {
      const name = n.name.getText();
      return name === 'sx' || name === 'style';
    }
    if ((ts.isVariableDeclaration(n) || ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n)) && n.name && ts.isIdentifier(n.name)) {
      // A style object returned by `getRowStyles()` is as much CSS as one
      // assigned to `rowStyles`; only the naming says so either way.
      return STYLE_NAME.test(n.name.text);
    }
    // A call such as `styled.div({...})` or `makeStyles({...})`.
    if (ts.isCallExpression(n) && /^(styled|makeStyles|css)\b/.test(n.expression.getText().split('(')[0])) return true;
  }
  return false;
}

/** The JSX tag a node is rendered inside, or null. */
function enclosingTag(node) {
  for (let n = node.parent; n; n = n.parent) {
    if (ts.isJsxSelfClosingElement(n) || ts.isJsxOpeningElement(n)) return n.tagName.getText();
  }
  return null;
}

/** Every colour in a CSS value that has a token, e.g. a `boxShadow` with two. */
function colorHits(text, tokens) {
  const hits = [];
  for (const [, raw] of text.matchAll(/(#[0-9a-fA-F]{3,8}\b|(?:rgba?|hsla?)\([^)]*\))/g)) {
    const token = tokens.colors.get(normalizeColor(raw));
    if (token) hits.push({ value: raw, token });
  }
  return hits;
}

/**
 * Token violations in one file.
 *
 * Keys are `category:value` and deliberately carry no line number, so the
 * caller can compare a file against its own base version as a multiset and
 * attribute only the *increase* to the change. Moving a line never counts.
 *
 * @returns {{ key: string, line: number, value: string, token: string, rule: string }[]}
 */
function findTokenViolations(source, file, tokens) {
  if (isTokenExempt(file)) return [];
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, file.endsWith('.ts') ? ts.ScriptKind.TS : ts.ScriptKind.TSX);
  const hits = [];

  const add = (node, category, value, token, rule) => {
    hits.push({ key: `${category}:${value}`, line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line, value, token, rule });
  };

  /** A property/attribute name plus its literal value, wherever it came from. */
  const checkValue = (node, name, literal, numeric) => {
    if (DIMENSION_PROPS.test(name)) return;
    const tag = enclosingTag(node);
    const inIcon = tag != null && ICON_TAG.test(tag);

    if (literal != null) {
      if (COLOR_PROPS.has(name) || /color/i.test(name)) {
        for (const hit of colorHits(literal, tokens)) add(node, 'color', hit.value.toLowerCase(), hit.token, '§1.8 colour');
        return;
      }
      // `padding: '8px 16px'` is two values in one string. Every part must map
      // to a token before anything is reported: half a suggestion
      // (`'8px 16px'` → `ds.space[2]`) reads as an instruction to drop the
      // other value, and `'2px 6px'` — where 6px is off-scale — is a shorthand
      // the system genuinely cannot express, so it is left alone.
      const parts = literal.trim().split(/\s+/);
      const scale = SPACING_PROPS.has(name)
        ? tokens.space
        : RADIUS_PROPS.has(name)
        ? tokens.radius
        : FONT_SIZE_PROPS.has(name) && !inIcon
        ? tokens.fontSize
        : null;
      if (!scale) return;
      const rule = scale === tokens.space ? '§1.8 spacing' : scale === tokens.radius ? '§1.8 radius' : '§1.8 font size';
      const category = scale === tokens.space ? 'space' : scale === tokens.radius ? 'radius' : 'text';
      const mapped = parts.map((part) => {
        const px = part.match(/^(\d+(?:\.\d+)?)px$/);
        return px && scale.has(Number(px[1])) ? scale.get(Number(px[1])) : null;
      });
      if (mapped.some((token) => token == null)) return;
      const value = parts.join(' ');
      const token = mapped.length === 1 ? mapped[0] : `\`${mapped.map((t) => `\${${t}}`).join(' ')}\``;
      add(node, category, value, token, rule);
      return;
    }

    if (numeric == null) return;
    if (FONT_WEIGHT_PROPS.has(name) && tokens.fontWeight.has(numeric)) {
      add(node, 'weight', String(numeric), tokens.fontWeight.get(numeric), '§1.8 font weight');
    } else if (SPACING_PROPS.has(name) && numeric !== 0) {
      // MUI spacing units: `mt: 2` is 16px, which is `--ds-space-4`. Negatives
      // are real (`mt: -0.5` pulls a caption up under its node) and need
      // `ds.space.mul(step, -1)` — §1.8's calc() helper. Plain negation of a
      // token would be `-ds.space[1]`, i.e. `-'var(--ds-space-1)'` → NaN.
      const px = Math.abs(numeric) * MUI_SPACING_UNIT;
      const step = tokens.space.get(px)?.match(/\[(\d+)\]$/)?.[1];
      if (step == null) return;
      const token = numeric < 0 ? `ds.space.mul(${step}, -1)` : tokens.space.get(px);
      add(node, 'space', `${numeric} (${numeric < 0 ? '-' : ''}${px}px)`, token, '§1.8 spacing');
    }
  };

  /** The literal text of a value node, or null when it is not a plain literal. */
  const literalOf = (node) => {
    if (ts.isStringLiteral(node)) return node.text;
    if (ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
    return null;
  };

  /**
   * The numeric value of a node. `-0.5` is not a numeric literal in the AST —
   * it is a `PrefixUnaryExpression` wrapping `0.5` — so reading only
   * `isNumericLiteral` silently skips every negative spacing unit.
   */
  const numericOf = (node) => {
    if (ts.isNumericLiteral(node)) return Number(node.text);
    if (ts.isPrefixUnaryExpression(node) && node.operator === ts.SyntaxKind.MinusToken && ts.isNumericLiteral(node.operand)) {
      return -Number(node.operand.text);
    }
    return null;
  };

  const visit = (node) => {
    if (ts.isPropertyAssignment(node) && isInsideStyleObject(node)) {
      const name = ts.isIdentifier(node.name) || ts.isStringLiteral(node.name) ? node.name.text : null;
      if (name) {
        const value = node.initializer;
        checkValue(node, name, literalOf(value), numericOf(value));
      }
    }
    if (ts.isJsxAttribute(node) && node.initializer && CSS_VALUE_ATTRS.has(node.name.getText())) {
      const init = node.initializer;
      const value = ts.isJsxExpression(init) ? init.expression : init;
      if (value) checkValue(node, node.name.getText(), literalOf(value), numericOf(value));
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return hits;
}

module.exports = { findTokenViolations, loadTokens, loadAccessors, isTokenExempt, normalizeColor };

if (require.main === module) {
  // Ad-hoc use: `node scripts/ds-tokens.js src/components/foo.tsx …`
  const appDir = path.resolve(__dirname, '..');
  const tokens = loadTokens(path.join(appDir, 'src/styles/theme-tokens.css'), path.join(appDir, 'src/utils/colors.ts'));
  for (const arg of process.argv.slice(2)) {
    const rel = path.relative(appDir, path.resolve(arg)).split(path.sep).join('/');
    for (const v of findTokenViolations(fs.readFileSync(arg, 'utf8'), rel, tokens)) {
      console.log(`${rel}:${v.line + 1}  ${v.value}  →  ${v.token}`);
    }
  }
}
