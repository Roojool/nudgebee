#!/usr/bin/env node
/**
 * PostToolUse hook: tell an agent about design-system drift in the `app/src`
 * file it just wrote, while it still has the context to fix it.
 *
 * `npm run lint2` already blocks this drift, but that is minutes and many tool
 * calls later, by which point fixing it means re-reading files and rebuilding
 * an argument the agent has already moved on from. Reporting at the edit makes
 * the correction a one-line follow-up instead.
 *
 * Node rather than a shell script on purpose: it needs no `jq` (which plenty of
 * dev machines lack), and none of the GNU-vs-BSD flag differences that keep
 * `session-start.sh` pinned to Linux. Node is already a hard dependency here.
 *
 * Always exits 0. A design-system opinion must never fail an agent's edit, and
 * a broken hook must never wedge the repo — every failure path is silent.
 */

const path = require('path');
const { execFileSync } = require('child_process');

const SOURCE_FILE = /\.(tsx?|jsx?)$/;

function main(payload) {
  const filePath = payload?.tool_input?.file_path || payload?.tool_response?.filePath;
  if (typeof filePath !== 'string' || !SOURCE_FILE.test(filePath)) return;

  // `CLAUDE_PROJECT_DIR` is the repo root; the checker lives under `app/`.
  const repoRoot = process.env.CLAUDE_PROJECT_DIR || path.resolve(__dirname, '../..');
  const checker = path.join(repoRoot, 'app/scripts/check-ds.js');

  let found;
  try {
    // A child process, not a require: the checker resolves `typescript` from
    // `app/node_modules`, and a require from here would also keep its parsed
    // token table alive for the life of the hook process.
    const raw = execFileSync(process.execPath, [checker, '--json', path.resolve(filePath)], {
      cwd: repoRoot,
      encoding: 'utf8',
      timeout: 20000,
      stdio: ['ignore', 'pipe', 'ignore'],
    });
    found = JSON.parse(raw || 'null');
  } catch {
    return; // not a checked file, no node_modules yet, not a git repo, … — stay quiet
  }
  if (!found || found.imports.length + found.values.length === 0) return;

  const lines = [
    `${found.imports.length + found.values.length} design-system violation(s) in the edit just made to ${found.file}:`,
    '',
    ...found.imports.map((v) => `  line ${v.line}: ${describeImport(v)} → use ${v.replacement}`),
    ...found.values.map((v) => `  line ${v.line}: hardcoded ${v.value} → use ${v.token} (${v.rule})`),
    '',
    'These are values the edit ADDED; anything the file already had is excluded.',
    'Fix them now — `npm run lint2` will fail on them otherwise. See app/design-system.md',
    '(§1.8 for the token scales, §3 for which primitive replaces which MUI component).',
    "Tokens come from `import { ds } from '@utils/colors';`. If the design system genuinely",
    'cannot express what this code needs, add `// ds-allow: <reason>` on the line instead.',
  ];

  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: { hookEventName: 'PostToolUse', additionalContext: lines.join('\n') },
    })
  );
}

function describeImport(v) {
  return v.key.startsWith('legacy:') ? `legacy ${v.key.slice(7)}` : `MUI ${v.key.split(':')[1]}`;
}

let stdin = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => (stdin += chunk));
process.stdin.on('end', () => {
  try {
    main(JSON.parse(stdin));
  } catch {
    // Malformed payload, or anything unforeseen: say nothing.
  }
});
