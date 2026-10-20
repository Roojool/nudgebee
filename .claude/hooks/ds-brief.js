#!/usr/bin/env node
/**
 * SessionStart hook: point an agent at the parts of `app/design-system.md` it
 * should read before writing frontend code.
 *
 * The doc is ~1,600 lines. "Read design-system.md" reliably produces one of two
 * failures: the agent skips it, or it loads the whole thing and spends a large
 * slice of its context on a per-component API reference it will not use. So
 * this names sections and line ranges instead, and says which are worth reading
 * up front versus looking up on demand.
 *
 * Once per session, not per edit. An earlier draft injected this before every
 * frontend Write/Edit; a single design-system cleanup run touches dozens of
 * files, and repeating the same block dozens of times dilutes attention rather
 * than directing it.
 *
 * Line numbers are resolved from the file's actual headings, so they stay right
 * as the doc grows. Exits 0 and says nothing if anything is missing.
 */

const fs = require('fs');
const path = require('path');

// Heading prefix → why an agent would open it, and whether to read it up front.
const SECTIONS = [
  ['## 1. Component index', 'which primitive exists for which purpose', true],
  ['## 1.8 Design system tokens', 'the colour / spacing / radius / type scales', true],
  ['## 3. Decision rules', 'which to pick when two components could fit', false],
  ['## 4. Component API reference', 'props and defaults — look up only what you use', false],
];

function main() {
  const repoRoot = process.env.CLAUDE_PROJECT_DIR || path.resolve(__dirname, '../..');
  const docPath = path.join(repoRoot, 'app/design-system.md');
  if (!fs.existsSync(docPath)) return;

  const lines = fs.readFileSync(docPath, 'utf8').split('\n');
  const located = SECTIONS.map(([heading, why, upFront]) => {
    const index = lines.findIndex((line) => line.startsWith(heading));
    return index === -1 ? null : { heading: lines[index].replace(/^#+\s*/, ''), line: index + 1, why, upFront };
  }).filter(Boolean);
  if (located.length === 0) return;

  const context = [
    'Design system — read before writing or editing UI under `app/`.',
    '',
    '`app/design-system.md` is the source of truth for which component to use and how to',
    'style it. It is ~1,600 lines, so read it by section rather than end to end:',
    '',
    ...located.map((s) => `  app/design-system.md:${s.line}  §${s.heading} — ${s.why}${s.upFront ? '  [read first]' : ''}`),
    '',
    'The two rules that get broken most: use `ds/*` primitives via `@ui/*` instead of raw',
    'MUI or hand-rolled equivalents, and never hardcode a colour, spacing, radius, font size',
    "or weight that has a `--ds-*` token — take it from `import { ds } from '@utils/colors';`.",
    'Both are enforced by `check-ds` in `npm run lint2`, on the values a change adds.',
  ].join('\n');

  process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: 'SessionStart', additionalContext: context } }));
}

try {
  main();
} catch {
  // A session must never fail to start because of a documentation pointer.
}
