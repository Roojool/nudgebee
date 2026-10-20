#!/usr/bin/env node
// Fails if a test matches an empty-table panel by its on-screen wording without
// also offering the id anchor as a rung.
//
// Why: EmptyData renders its heading as <h2 id="${tableId}-no-data">. That id is
// ours; the wording is the product's, and the product changes it — on 2026-09-17
// CustomTable stopped hard-coding "All good here!" so each table could say
// something accurate ("No open vulnerabilities" rather than a false clean bill of
// health). Every test pinned to the old string broke, and the whole
// CloudAccount/Vulnerabilities suite went down for days before anyone noticed it
// was the copy and not the feature. Text is fine as a fallback rung; text alone
// is a test that expires the next time someone improves the UI.

const fs = require("fs");
const path = require("path");

// The headings CustomTable renders when a caller supplies no copy of its own.
// Matching these is the signal; any other string is the caller's own wording and
// is out of scope for this gate.
const FIXED_EMPTY_COPY = ["All good here!", "No recommendations available"];
const ID_ANCHOR = "-no-data";
const TESTS_DIR = path.resolve(__dirname, "..", "tests");

// A line that only talks about the copy is documentation, not a locator. Both
// forms appear throughout these files precisely because the behaviour needed
// explaining, so counting them would fail every file that did the right thing.
const isComment = (line) => /^\s*(\/\/|\*|\/\*)/.test(line.trim());

function walk(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, out);
    else if (entry.name.endsWith(".ts")) out.push(full);
  }
  return out;
}

// A locator chain spans a handful of lines, so the id rung is looked for beside
// the wording rather than anywhere in the file: one correctly anchored locator
// must not excuse an unanchored one further down the same file.
const WINDOW = 6;

// What actually resolves a node. Declaring the copy as a constant is fine — it is
// the locator built from it that has to carry the id rung, and these files name
// the string once and use it from several places.
const LOCATOR_CALL = /getBy(Role|Text|Label|Title|TestId)|hasText|locator\(/;

// `const NO_DATA_HEADING = "All good here!"` means later locators reference the
// name rather than the literal, so the name has to count as the copy too.
function copyAliases(text) {
  const names = [];
  const re = /(?:const|let|var)\s+([A-Za-z0-9_]+)\s*=\s*["'`]([^"'`]+)["'`]/g;
  let m;
  while ((m = re.exec(text)) !== null) {
    if (FIXED_EMPTY_COPY.includes(m[2])) names.push(m[1]);
  }
  return names;
}

const offenders = [];
for (const file of walk(TESTS_DIR)) {
  const text = fs.readFileSync(file, "utf8");
  const lines = text.split("\n");
  const tokens = [...FIXED_EMPTY_COPY, ...copyAliases(text)];
  lines.forEach((line, i) => {
    if (isComment(line) || !LOCATOR_CALL.test(line)) return;
    if (!tokens.some((token) => line.includes(token))) return;
    const near = lines.slice(Math.max(0, i - WINDOW), i + WINDOW + 1).join("\n");
    if (near.includes(ID_ANCHOR)) return;
    offenders.push(`${path.relative(TESTS_DIR, file)}:${i + 1}`);
  });
}

if (offenders.length) {
  console.error("Empty-state panels matched by wording alone:\n");
  offenders.forEach((o) => console.error(`  ${o}`));
  console.error(
    [
      "",
      "Lead with the id and keep the wording as a fallback, e.g.:",
      "",
      '  scope.locator(`#${TABLE_ID}-no-data`)',
      '    .or(scope.getByRole("heading", { name: HEADING, exact: true }))',
      "    .first()",
      "",
      "Where several tables in one scope supply different copy, match the id",
      'shape instead: scope.locator(\'[id$="-no-data"]\').',
      "",
    ].join("\n")
  );
  process.exit(1);
}

console.log("Empty-state panels: no test relies on wording alone.");
