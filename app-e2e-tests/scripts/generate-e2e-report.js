// Builds a single self-contained HTML report from the run data written by
// notifications/E2EReportReporter.ts, styled to match
// llm/benchmark/benchmark_server/static/index.html.
//
// Usage: node scripts/generate-e2e-report.js [resultsFile] [outFile] [env]
const fs = require("fs");
const path = require("path");

const resultsFile = process.argv[2] || path.join("playwright-report", "e2e-results.json");
const outFile = process.argv[3] || "e2e-report.html";
const envName = process.argv[4] || "dev";
// CI wipes the report folder at the start of every run, so the trend has to be
// kept somewhere that survives it — E2E_REPORT_HISTORY points there.
const historyFile =
  process.env.E2E_REPORT_HISTORY || path.join(path.dirname(outFile), "e2e-report-history.json");

// Playwright auto-generates a step for every API call it makes. Those are the
// execution trace, not the intent — the steps worth reading are the ones the
// spec author named with test.step(), which are the only ones carrying prose
// names AND child steps. A custom expect() message also gets a prose name but
// never has children, which is how the two are told apart.
const AUTO_STEP =
  /^(Before Hooks|After Hooks|Register locator handler|Navigate to|Wait for|Expect |Mouse |Click |Fill |Press |Type |Set |Hover|Check |Uncheck|Select |Query |Focus|Blur|Close|Create|Launch|Screenshot|screenshot|error-context|trace|Scroll|Drag|Tap|Evaluate|Reload|Go back|Go forward)/;

// Actions that say nothing about intent — cursor parking, arbitrary sleeps,
// load-state waits. Dropping them is what makes the fallback list readable.
const DEAD_ACTION =
  /^(Mouse move|Wait for timeout|Wait for load state|Wait for navigation|Press "Escape"|Register locator handler|Screenshot|screenshot|error-context|trace)/;

// Playwright's own step names are machine-shaped. These rewrites keep the
// meaning but read as sentences.
function humanizeAction(name) {
  let m = name.match(/^Navigate to "(.+)"$/);
  if (m) return "Open " + m[1];
  m = name.match(/^Expect "(?:to)?([A-Za-z]+)" (.+)$/);
  if (m) {
    const check = m[1].replace(/([A-Z])/g, " $1").trim().toLowerCase();
    return "Expect " + shortLocator(m[2]) + " to " + check;
  }
  m = name.match(/^(Click|Fill|Hover|Check|Uncheck|Select|Focus|Tap|Drag) (.+)$/);
  if (m) return m[1] + " " + shortLocator(m[2]);
  m = name.match(/^Wait for selector (.+)$/);
  if (m) return "Wait for " + shortLocator(m[1]);
  return name;
}

// locator('#id') / getByRole('tab', { name: 'X' }) -> the part a human reads.
function shortLocator(s) {
  let m = s.match(/locator\('([^']+)'\)/);
  if (m) return m[1];
  m = s.match(/getBy\w+\('([^']+)'/);
  if (m) return '"' + m[1] + '"';
  m = s.match(/name:\s*'([^']+)'/);
  if (m) return '"' + m[1] + '"';
  return s.length > 60 ? s.slice(0, 60) + "…" : s;
}

// Failure screenshots ride along as step attachments. Inlining them keeps the
// report a single portable file.
function findScreenshot(raw) {
  let found = null;
  (function walk(steps) {
    for (const s of steps || []) {
      for (const a of s.attachments || []) {
        if (!found && (a.type || "").indexOf("image") === 0) found = a.source;
      }
      walk(s.steps);
    }
  })(raw.steps);
  for (const a of raw.attachments || []) {
    if (!found && (a.type || "").indexOf("image") === 0) found = a.source;
  }
  // The file sits in test-results/ on the same machine, but a missing or
  // unreadable screenshot must not take the whole report down with it.
  if (!found) return "";
  try {
    return "data:image/png;base64," + fs.readFileSync(found).toString("base64");
  } catch (e) {
    return "";
  }
}

function extractSteps(raw) {
  const top = (raw.steps || []).filter((s) => s.status);
  const named = top.filter((s) => !AUTO_STEP.test(s.name || "") && (s.steps || []).length);
  if (named.length) {
    const items = [];
    for (const s of named) {
      items.push({ name: s.name || "", status: s.status, duration: (s.stop || 0) - (s.start || 0) });
      // Only a failed step gets expanded into its actions — that is where the
      // "which click actually broke" answer lives, and doing it for passing
      // steps would bury the list again.
      if (s.status !== "passed") {
        for (const child of s.steps || []) {
          if (DEAD_ACTION.test(child.name || "")) continue;
          items.push({
            name: humanizeAction(child.name || ""),
            status: child.status,
            duration: (child.stop || 0) - (child.start || 0),
            child: true,
          });
        }
      }
    }
    return { kind: "named", items: items };
  }
  // No authored steps — fall back to the action trace so the row still shows
  // what the test actually did, clearly labelled as the weaker substitute.
  return {
    kind: "actions",
    items: top
      .filter((s) => !/^(Before Hooks|After Hooks)/.test(s.name || "") && !DEAD_ACTION.test(s.name || ""))
      .map((s) => ({
        name: humanizeAction(s.name || ""),
        status: s.status,
        duration: (s.stop || 0) - (s.start || 0),
      })),
  };
}

function readResults(file) {
  const attempts = {};
  for (const raw of JSON.parse(fs.readFileSync(file, "utf8"))) {
    // A retry is the same test again, so the file path and title together are
    // what identify it across attempts.
    const key = raw.suite + "::" + raw.name;
    (attempts[key] = attempts[key] || []).push(raw);
  }

  return Object.values(attempts).map((runs) => {
    runs.sort((a, b) => (a.retry || 0) - (b.retry || 0));
    const final = runs[runs.length - 1];
    return {
      name: final.name || "",
      status: final.status || "unknown",
      // Playwright retries a failure; landing on passed after an earlier
      // failure is the definition of flaky, and it is the number the team
      // actually acts on — a green suite hiding retries is not green.
      flaky: final.status === "passed" && runs.some((r) => r.status !== "passed"),
      retries: runs.length - 1,
      duration: (final.stop || 0) - (final.start || 0),
      start: runs[0].start || 0,
      tags: final.tags || [],
      suite: final.suite || "",
      message: (final.statusDetails || {}).message || "",
      trace: (final.statusDetails || {}).trace || "",
      // Same rule the Slack reporter uses (SlackReporter.isApiFailure): a
      // failure carrying a [GraphQLWatcher] error is a backend/API failure and
      // is reported apart from ordinary test failures, so the two summaries
      // can't disagree.
      apiFailure:
        final.status !== "passed" &&
        ((final.statusDetails || {}).message || "").indexOf("[GraphQLWatcher]") !== -1,
      // A clean pass does not need its raw action trace: nobody reads what a
      // green test clicked, and those traces are most of the report's size --
      // 14MB for the 1082-test nightly, past what Slack will preview. Authored
      // test.step() lists stay, for every outcome.
      steps: (function (st) {
        const cleanPass = final.status === "passed" && !runs.some((r) => r.status !== "passed");
        return cleanPass && st.kind === "actions" ? { kind: st.kind, items: [] } : st;
      })(extractSteps(final)),
      screenshot: final.status === "passed" ? "" : findScreenshot(final),
    };
  });
}

function percentile(sorted, p) {
  if (!sorted.length) return 0;
  const idx = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1);
  return sorted[Math.max(0, idx)];
}

function summarise(tests) {
  const durations = tests.map((t) => t.duration).sort((a, b) => a - b);
  const count = (s) => tests.filter((t) => t.status === s).length;
  const passed = count("passed");
  const apiFailures = tests.filter((t) => t.apiFailure).length;
  const retried = tests.filter((t) => t.retries > 0);
  return {
    total: tests.length,
    passed: passed,
    // Excludes API failures, which are counted and listed on their own —
    // mirrors the Slack summary so the two never report different numbers.
    failed: count("failed") + count("broken") - apiFailures,
    apiFailures: apiFailures,
    broken: 0,
    skipped: count("skipped"),
    retried: retried.length,
    retryPassed: retried.filter((t) => t.status === "passed").length,
    retryFailed: retried.filter((t) => t.status !== "passed").length,
    flaky: tests.filter((t) => t.flaky).length,
    passRate: tests.length ? (passed / tests.length) * 100 : 0,
    totalMs: durations.reduce((a, b) => a + b, 0),
    avgMs: durations.length ? durations.reduce((a, b) => a + b, 0) / durations.length : 0,
    p95Ms: percentile(durations, 95),
    maxMs: durations.length ? durations[durations.length - 1] : 0,
  };
}

function byTag(tests) {
  const map = {};
  for (const t of tests) {
    for (const tag of t.tags) {
      const row = (map[tag] = map[tag] || { tag: tag, total: 0, passed: 0, failed: 0, flaky: 0, green: 0, ms: 0 });
      row.total++;
      // Same rule as the tiles: the three columns are exclusive and sum to
      // total, while the rate counts flaky as green.
      if (t.flaky) row.flaky++;
      else if (t.status === "passed") row.passed++;
      else row.failed++;
      if (t.status === "passed") row.green++;
      row.ms += t.duration;
    }
  }
  return Object.values(map)
    .map((r) => Object.assign(r, { rate: r.total ? (r.green / r.total) * 100 : 0, avgMs: r.ms / r.total }))
    .sort((a, b) => a.rate - b.rate || b.total - a.total);
}

// Quarantined tests are excluded from every run (--grep-invert @quarantine),
// so they never appear in results — reading the source is the only way to
// report them at all. Matches test("title", { tag: [... "@quarantine"] }),
// whether the declaration sits on one line or wraps.
function findQuarantined(dir, rootDir, out) {
  out = out || [];
  rootDir = rootDir || dir;
  if (!fs.existsSync(dir)) return out;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      findQuarantined(full, rootDir, out);
    } else if (entry.name.endsWith(".spec.ts")) {
      const src = fs.readFileSync(full, "utf8");
      // test.skip(...) counts too: several quarantined tests are hard-skipped
      // in source rather than only tagged, and matching bare test( alone
      // silently undercounts them.
      const re = /test(\.skip|\.fixme|\.only)?\(\s*(["'`])((?:\\.|(?!\2)[\s\S])*?)\2\s*,\s*\{[^}]*?"@quarantine"/g;
      let m;
      while ((m = re.exec(src)) !== null) {
        out.push({
          name: m[3].replace(/\s+/g, " ").trim(),
          file: path.relative(rootDir, full),
          skipped: m[1] === ".skip" || m[1] === ".fixme",
        });
      }
    }
  }
  return out;
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
  });
}

// Titles come from test("...") and follow two conventions in this suite:
// "Module -> Sub -> Case" and "Module - what it does, verify ...". Splitting
// the module path off the intent lets the list be scanned by intent instead
// of by the repeated prefix every row in a file shares.
function splitTitle(name) {
  const arrow = name.split(/\s*-+>\s*/);
  if (arrow.length > 1) {
    return { context: arrow.slice(0, -1).join(" › ").trim(), intent: arrow[arrow.length - 1].trim() };
  }
  const dash = name.match(/^([^-]{3,40}?)\s+-\s+(.*)$/s);
  if (dash) return { context: dash[1].trim(), intent: dash[2].trim() };
  return { context: "", intent: name };
}

const secs = (ms) => (ms / 1000).toFixed(1) + "s";
const mins = (ms) => (ms >= 60000 ? (ms / 60000).toFixed(1) + "m" : secs(ms));

// Pinned rather than left to the machine locale: the report is generated on a
// CI runner and read in India, and the Slack summary for the same run already
// stamps IST -- two different times for one run is a support question.
const IST = "Asia/Kolkata";
const fmtDateTime = (t) => new Date(t).toLocaleString("en-IN", { timeZone: IST });
const fmtDate = (t) => new Date(t).toLocaleDateString("en-IN", { timeZone: IST });

const tests = readResults(resultsFile);
const startTimes = tests.map((t) => t.start).filter(Boolean);
const runStartedAt = startTimes.length ? startTimes.reduce((a, b) => (b < a ? b : a)) : Date.now();
const stats = summarise(tests);
const tags = byTag(tests);
// …/app-e2e-tests/playwright-report/e2e-results.json -> …/app-e2e-tests/tests
const testsDir = path.join(path.dirname(path.dirname(path.resolve(resultsFile))), "tests");
const quarantinedTests = findQuarantined(testsDir);
const quarantined = quarantinedTests.length;

// Trend is just each run's summary appended to a file the next run reads —
// no separate history mechanism to carry between builds.
let history = [];
if (fs.existsSync(historyFile)) {
  try {
    // Restored from a CI cache, so it is not necessarily the array it was.
    const parsed = JSON.parse(fs.readFileSync(historyFile, "utf8"));
    history = Array.isArray(parsed) ? parsed : [];
  } catch (e) {
    history = [];
  }
}
// Outcome of the previous run. It must skip any entry for THIS run, which a
// re-generation from the same results would otherwise leave on record —
// comparing a run against itself reports every failure as already-known.
const thisRunStamp = new Date(runStartedAt).toISOString();
const priorRuns = history.filter((h) => h.when !== thisRunStamp);
const previous = priorRuns.length ? priorRuns[priorRuns.length - 1] : null;

const outcomeOf = (t) =>
  t.flaky ? "flaky" : t.apiFailure ? "apifailed" : t.status;

// Per-test outcomes are stored, not just the totals: the totals give the
// trend line, the per-test map gives new-vs-known and flaky frequency.
const testOutcomes = {};
for (const t of tests) testOutcomes[t.name] = outcomeOf(t);

// Regenerating from the same results is not a new run. Replacing the entry
// keeps the comparison pointing at the genuinely previous execution.
history = history.filter((h) => h.when !== thisRunStamp);

history.push({
  // The run's own start time, not when this script happened to run — the
  // report is often generated well after the tests finished.
  when: new Date(runStartedAt).toISOString(),
  passed: stats.passed,
  failed: stats.failed + stats.apiFailures,
  flaky: stats.flaky,
  passRate: Number(stats.passRate.toFixed(1)),
  tests: testOutcomes,
});
history = history.slice(-20);
// A partial run still shows the trend, but must not join it: a 9-test affected
// run recorded next to a 1000-test nightly makes the line meaningless and the
// against-previous comparison wrong.
if (!process.env.E2E_REPORT_TREND_READONLY) {
  fs.mkdirSync(path.dirname(historyFile), { recursive: true });
  fs.writeFileSync(historyFile, JSON.stringify(history, null, 2));
}

const isBad = (o) => o === "failed" || o === "apifailed" || o === "broken";

// Three buckets, because they call for different reactions: a regression to
// investigate now, a known breakage, and a test the previous run never
// exercised (so there is nothing to compare it against).
const failingNow = tests.filter((t) => isBad(outcomeOf(t)));
const newFailures = [];
const knownFailures = [];
const unseenBefore = [];
for (const t of failingNow) {
  if (!previous || !previous.tests) unseenBefore.push(t);
  else if (!(t.name in previous.tests)) unseenBefore.push(t);
  else if (isBad(previous.tests[t.name])) knownFailures.push(t);
  else newFailures.push(t);
}

// How often each test has flaked across the runs on record. One flake is
// noise; the same test flaking repeatedly is the thing to act on.
const flakeCounts = {};
for (const h of history) {
  for (const name in h.tests || {}) {
    if (h.tests[name] === "flaky") flakeCounts[name] = (flakeCounts[name] || 0) + 1;
  }
}
const repeatFlakes = Object.keys(flakeCounts)
  .map((name) => ({ name: name, count: flakeCounts[name] }))
  .sort((a, b) => b.count - a.count);

// Failures grouped by what actually broke. Rules are ordered most specific
// first; the fallback keeps the raw first line so nothing is silently
// swallowed into a catch-all.
const CAUSE_RULES = [
  [/\[GraphQLWatcher\]/, "Backend / GraphQL query failed"],
  [/intercepts pointer events/, "Click blocked by an overlay or backdrop"],
  [/Test timeout of \d+ms exceeded/, "Test exceeded its overall timeout"],
  [/page\.waitForURL/, "Navigation never reached the expected URL"],
  [/locator\.click.*Timeout|Timeout.*locator\.click/, "Click timed out — element never became actionable"],
  [/waitForSelector|waitFor\(.*Timeout/, "Waited for an element that never appeared"],
  [/toBeVisible/, "Element never became visible"],
  [/toHaveText|toContainText/, "Text did not match what was expected"],
  [/toHaveCount/, "Unexpected number of elements"],
  [/toHaveAttribute|toHaveValue/, "Attribute or value did not match"],
  [/net::ERR|ECONNREFUSED|Navigation failed|net::/, "Network or navigation failure"],
  [/strict mode violation/, "Locator matched more than one element"],
];

function causeOf(t) {
  const msg = t.message || t.trace || "";
  for (const rule of CAUSE_RULES) if (rule[0].test(msg)) return rule[1];
  const first = msg.split("\n").map((l) => l.trim()).filter(Boolean)[0] || "Unclassified failure";
  return first.slice(0, 80);
}

const causeMap = {};
for (const t of failingNow) {
  const c = causeOf(t);
  (causeMap[c] = causeMap[c] || []).push(t);
}
const causes = Object.keys(causeMap)
  .map((c) => ({ cause: c, tests: causeMap[c], count: causeMap[c].length }))
  .sort((a, b) => b.count - a.count);

const slowest = tests.slice().sort((a, b) => b.duration - a.duration).slice(0, 10);

// Module = the spec's folder, which maps to a product area. The filename
// alone is too granular and the top folder alone is too coarse.
const moduleMap = {};
for (const t of tests) {
  const parts = String(t.suite || "").split(/[\\/]/);
  const mod = parts.length > 1 ? parts.slice(0, -1).join(" / ") : parts[0] || "root";
  const row = (moduleMap[mod] = moduleMap[mod] || { mod: mod, total: 0, passed: 0, failed: 0, flaky: 0, ms: 0 });
  row.total++;
  const o = outcomeOf(t);
  if (o === "flaky") row.flaky++;
  else if (o === "passed") row.passed++;
  else row.failed++;
  row.ms += t.duration;
}
const modules = Object.keys(moduleMap)
  .map((m) =>
    Object.assign(moduleMap[m], {
      rate: moduleMap[m].total ? ((moduleMap[m].passed + moduleMap[m].flaky) / moduleMap[m].total) * 100 : 0,
      avgMs: moduleMap[m].ms / moduleMap[m].total,
    })
  )
  .sort((a, b) => a.rate - b.rate || b.total - a.total);

// Bee mark from app/src/assets/images/NB-logo-with-text.svg (wordmark path
// dropped). Square viewBox so it renders cleanly at favicon size.
const faviconSvg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-1 -1 35 35">
<path d="M10.4581 14.8555C10.4581 14.8555 9.94265 23.6184 15.7417 30.0618C15.7417 30.0618 9.44157 31.5602 7.10758 24.1339C4.98127 17.3684 10.4581 14.8555 10.4581 14.8555Z" fill="#FACF39"/>
<path d="M17.6273 13.6992C17.6273 13.6992 17.1782 21.3331 22.23 26.9462C22.23 26.9462 16.7417 28.2516 14.7085 21.7821C12.8561 15.8883 17.6273 13.6992 17.6273 13.6992Z" fill="#FACF39"/>
<path d="M4.33541 20.3789C4.33541 20.3789 3.98654 26.3096 7.91129 30.6705C7.91129 30.6705 4.17414 30.901 2.57941 26.4873C1.39113 23.1985 4.33541 20.3789 4.33541 20.3789Z" fill="#FACF39"/>
<path d="M23.6997 11.4089C23.6997 11.4089 10.7486 -4.82841 1.85673 2.32372C-7.03511 9.47585 23.6997 11.4089 23.6997 11.4089Z" fill="#FACF39"/>
<path d="M28.195 21.7298C30.4125 21.7298 32.2102 19.9321 32.2102 17.7145C32.2102 15.4969 30.4125 13.6992 28.195 13.6992C25.9774 13.6992 24.1797 15.4969 24.1797 17.7145C24.1797 19.9321 25.9774 21.7298 28.195 21.7298Z" fill="#FACF39"/>
</svg>`;
const faviconUri = "data:image/svg+xml;base64," + Buffer.from(faviconSvg).toString("base64");

const sColor = (v) => (v >= 90 ? "#10B981" : v >= 70 ? "#F59E0B" : "#EF4444");

const card = (lbl, value, color) =>
  `<div class="bg-white border border-gray-100 rounded-xl p-3 text-center shadow-sm">
    <div class="text-[9px] font-semibold text-gray-400 uppercase tracking-wide mb-1">${esc(lbl)}</div>
    <div class="text-[14px] font-bold" style="color:${color || "#374151"}">${value}</div>
  </div>`;

const section = (title, note) =>
  `<div class="text-[12px] font-semibold text-gray-600 mb-1.5 mt-2 border-b border-gray-200 pb-1">${esc(title)}` +
  (note ? `<span class="text-[10px] font-normal text-gray-400 ml-1">${esc(note)}</span>` : "") +
  `</div>`;

// Playwright's message opens with the assertion/timeout line and then dumps
// the whole retry call log, which buries the one line that says what broke.
function headline(t) {
  const raw = (t.message || t.trace || "").split("\n").map((l) => l.trim()).filter(Boolean);
  const first = raw.find((l) => !l.startsWith("- ") && !l.startsWith("Call log"));
  return (first || raw[0] || "").slice(0, 300);
}

const statusPill = (t) => {
  if (t.flaky) return '<span class="text-[11px] font-semibold text-amber-500">FLAKY</span>';
  if (t.apiFailure) return '<span class="text-[11px] font-semibold text-red-600">API FAIL</span>';
  const map = {
    passed: '<span class="text-[11px] font-semibold text-green-600">PASS</span>',
    failed: '<span class="text-[11px] font-semibold text-red-500">FAIL</span>',
    broken: '<span class="text-[11px] font-semibold text-yellow-600">BROKEN</span>',
    skipped: '<span class="text-[11px] font-semibold text-gray-400">SKIPPED</span>',
  };
  return map[t.status] || '<span class="text-[11px] text-gray-400">' + esc(t.status) + "</span>";
};

// Who/what started this run. GitHub sets these in the job; running locally
// they are simply absent. The actor is only meaningful for a manual dispatch —
// on a scheduled run it is the automation, not a person, so it is not shown.
const env = process.env;
const isDispatch = env.GITHUB_EVENT_NAME === "workflow_dispatch";
const runUrl =
  env.GITHUB_SERVER_URL && env.GITHUB_REPOSITORY && env.GITHUB_RUN_ID
    ? `${env.GITHUB_SERVER_URL}/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}`
    : "";
// A dispatch with no selection at all is a full-suite run; anything else
// records exactly what the person narrowed it to.
const selection = [
  ["Module", env.INPUT_MODULE],
  ["Test file", env.INPUT_TEST_FILE],
  ["Test name", env.INPUT_TEST_NAME],
  ["Tag", env.INPUT_TAG],
].filter((s) => s[1] && String(s[1]).trim());

const chip = (lbl, value, accent) =>
  `<span class="inline-flex items-center gap-1 text-[10px] px-2 py-1 rounded-md border ${
    accent ? "border-nb-yellow/40 bg-amber-50 text-amber-800" : "border-gray-200 bg-white text-gray-600"
  }">
    <span class="uppercase tracking-wide text-[9px] opacity-70">${esc(lbl)}</span>
    <span class="font-semibold">${esc(value)}</span>
  </span>`;

let runContextHtml = "";
if (isDispatch) {
  runContextHtml = `<div class="flex flex-wrap items-center gap-2 mb-4 p-2.5 rounded-xl border border-gray-200 bg-white shadow-sm">
    <span class="material-icons-outlined text-[16px]" style="color:#FB923C">play_circle</span>
    <span class="text-[11px] font-semibold text-gray-700">Manually triggered</span>
    ${chip("By", "@" + (env.GITHUB_TRIGGERING_ACTOR || env.GITHUB_ACTOR || "unknown"), true)}
    ${env.GITHUB_REF_NAME ? chip("Branch", env.GITHUB_REF_NAME) : ""}
    ${env.GITHUB_RUN_NUMBER ? chip("Run", "#" + env.GITHUB_RUN_NUMBER) : ""}
    ${selection.length ? selection.map((s) => chip(s[0], s[1])).join("") : chip("Scope", "Full suite")}
    ${
      runUrl
        ? `<a href="${esc(runUrl)}" target="_blank" rel="noopener"
             class="text-[10px] text-blue-500 hover:underline ml-auto">Open CI run &rarr;</a>`
        : ""
    }
  </div>`;
} else if (runUrl) {
  // Automated run: no person to attribute it to, so none is shown.
  runContextHtml = `<div class="flex flex-wrap items-center gap-2 mb-4 p-2.5 rounded-xl border border-gray-200 bg-white shadow-sm">
    <span class="material-icons-outlined text-[16px]" style="color:#60A5FA">schedule</span>
    <span class="text-[11px] font-semibold text-gray-700">Automated run</span>
    ${chip("Event", env.GITHUB_EVENT_NAME || "unknown")}
    ${env.GITHUB_REF_NAME ? chip("Branch", env.GITHUB_REF_NAME) : ""}
    ${env.GITHUB_RUN_NUMBER ? chip("Run", "#" + env.GITHUB_RUN_NUMBER) : ""}
    <a href="${esc(runUrl)}" target="_blank" rel="noopener"
       class="text-[10px] text-blue-500 hover:underline ml-auto">Open CI run &rarr;</a>
  </div>`;
} else {
  runContextHtml = `<div class="flex items-center gap-2 mb-4 p-2.5 rounded-xl border border-gray-200 bg-white shadow-sm">
    <span class="material-icons-outlined text-[16px]" style="color:#9CA3AF">computer</span>
    <span class="text-[11px] text-gray-500">Local run &mdash; not triggered from CI</span>
  </div>`;
}

const headerHtml = `<div class="grid grid-cols-5 gap-3 mb-4">
  ${card("Total Tests", stats.total, "#3B82F6")}
  ${card("Timestamp", fmtDateTime(runStartedAt), "#6B7280")}
  ${card("Environment", esc(envName.toUpperCase()), "#F59E0B")}
  ${card("Duration", mins(stats.totalMs), "#8B5CF6")}
  ${card("Quarantined", quarantined + " excluded", "#6B7280")}
</div>`;

// One stacked bar of the actual split. A grouped chart here repeated the
// tiles above (pass rate and passed % are the same number), which is noise.
const pct = (n) => (stats.total ? (n / stats.total) * 100 : 0);
const cleanPassed = stats.passed - stats.flaky;
const segments = [
  { l: "Passed", n: cleanPassed, c: "#10B981" },
  { l: "Flaky", n: stats.flaky, c: "#F59E0B" },
  { l: "Failed", n: stats.failed, c: "#EF4444" },
  { l: "API Failures", n: stats.apiFailures, c: "#DC2626" },
  { l: "Skipped", n: stats.skipped, c: "#D1D5DB" },
].filter((s) => s.n > 0);

const filterCard = (lbl, value, color, filter, hint) =>
  `<div onclick="filterTests('${filter}')" title="${esc(hint || "")}"
        class="bg-white border border-gray-100 rounded-xl p-3 text-center shadow-sm cursor-pointer hover:border-gray-300 hover:shadow transition">
    <div class="text-[9px] font-semibold text-gray-400 uppercase tracking-wide mb-1">${esc(lbl)}</div>
    <div class="text-[14px] font-bold" style="color:${color}">${value}</div>
    <div class="text-[8px] text-gray-300 mt-0.5">click to filter</div>
  </div>`;

const resultsHtml = `${section(
  "Results",
  "(click a tile to filter — the five add up to " + stats.total + ", same definitions as the Slack summary)"
)}
<div class="grid grid-cols-6 gap-3 mb-2">
  ${card("Pass Rate", stats.passRate.toFixed(1) + "%", sColor(stats.passRate))}
  ${filterCard("Passed", cleanPassed, "#10B981", "passed", "Passed first time, no retry needed")}
  ${filterCard("Failed", stats.failed, stats.failed ? "#EF4444" : "#9CA3AF", "failed", "Test failures, excluding API failures which are counted separately")}
  ${filterCard("API Failures", stats.apiFailures, stats.apiFailures ? "#DC2626" : "#9CA3AF", "apifailed", "Backend/GraphQL failures caught by GraphQLWatcher — a broken API, not a broken test")}
  ${filterCard("Flaky", stats.flaky, stats.flaky ? "#F59E0B" : "#9CA3AF", "flaky", "Failed first, then passed on retry — same code, same environment, different result")}
  ${filterCard("Skipped", stats.skipped, "#9CA3AF", "skipped", "Tests skipped this run")}
</div>
<div class="grid grid-cols-3 gap-3 mb-2">
  ${filterCard("Retried", stats.retried, stats.retried ? "#F59E0B" : "#9CA3AF", "retried",
    "Ran more than once because the first attempt failed")}
  ${filterCard("Retry Passed", stats.retryPassed, stats.retryPassed ? "#F59E0B" : "#9CA3AF", "retrypassed",
    "Failed, then passed on the retry — these are the same tests as Flaky")}
  ${filterCard("Retry Failed", stats.retryFailed, stats.retryFailed ? "#EF4444" : "#9CA3AF", "retryfailed",
    "Retried and still failed — counted under Failed or API Failures")}
</div>
<div class="bg-white border border-gray-100 rounded-xl p-4 shadow-sm mb-4">
  <div class="flex w-full h-5 rounded-md overflow-hidden">
    ${segments
      .map(
        (s) =>
          `<div style="width:${pct(s.n)}%;background:${s.c}" title="${s.l}: ${s.n}"></div>`
      )
      .join("")}
  </div>
  <div class="flex gap-4 mt-2.5">
    ${segments
      .map(
        (s) => `<div class="flex items-center gap-1.5">
      <span class="inline-block w-2.5 h-2.5 rounded-sm" style="background:${s.c}"></span>
      <span class="text-[10px] text-gray-500">${s.l} <span class="font-semibold text-gray-700">${s.n}</span>
      <span class="text-gray-400">(${pct(s.n).toFixed(1)}%)</span></span>
    </div>`
      )
      .join("")}
  </div>
</div>`;

const durationHtml = `${section("Duration")}
<div class="grid grid-cols-3 gap-3 mb-4">
  ${card("Avg / Test", secs(stats.avgMs))}
  ${card("P95", secs(stats.p95Ms))}
  ${filterCard("Slowest", secs(stats.maxMs), "#374151", "slowest", "Show the 10 longest-running tests in the list below")}
</div>`;

const TAG_COLS = "grid-cols-[1.6fr_0.5fr_0.4fr_0.4fr_0.4fr_0.4fr_0.5fr]";
const tagHtml = tags.length
  ? `${section("Results by Tag", "(click a tag to see its tests — a test carries several tags, so counts overlap)")}
<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-4">
  <div class="grid ${TAG_COLS} gap-2 px-3 py-1.5 text-[9px] font-semibold text-gray-400 uppercase tracking-wide bg-gray-50 border-b border-gray-100">
    <span>Tag</span><span class="text-center">Pass Rate</span><span class="text-center">Total</span><span class="text-center">Passed</span>
    <span class="text-center">Failed</span><span class="text-center">Flaky</span><span class="text-center">Avg Time</span>
  </div>
  <div class="flex flex-col gap-[1px]">
    ${tags
      .map(
        (r) => `<div onclick="filterTests('tag-${esc(r.tag)}')"
           class="grid ${TAG_COLS} gap-2 px-3 py-1.5 bg-white border border-gray-100 rounded-md items-center cursor-pointer hover:bg-gray-50">
      <span class="text-[11px] font-medium text-gray-700">@${esc(r.tag)}</span>
      <span class="text-[11px] font-semibold text-center" style="color:${sColor(r.rate)}">${r.rate.toFixed(1)}%</span>
      <span class="text-[11px] text-center font-semibold text-gray-700">${r.total}</span>
      <span class="text-[11px] text-center text-gray-600">${r.passed}</span>
      <span class="text-[11px] text-center ${r.failed ? "text-red-500 font-semibold" : "text-gray-400"}">${r.failed}</span>
      <span class="text-[11px] text-center ${r.flaky ? "text-amber-500 font-semibold" : "text-gray-400"}">${r.flaky}</span>
      <span class="text-[11px] text-center text-gray-500">${secs(r.avgMs)}</span>
    </div>`
      )
      .join("")}
  </div>
</div>`
  : "";

// Failures first: the report exists to get someone to the broken test.
const ordered = tests.slice().sort((a, b) => {
  const rank = (t) => (t.status !== "passed" ? 0 : t.flaky ? 1 : 2);
  return rank(a) - rank(b) || b.duration - a.duration;
});

const TEST_COLS = "grid-cols-[2.6fr_0.5fr_0.5fr]";
const testHtml = `${section("Test Details", "(failures and flaky tests first — click a row for the error)")}
<div id="filter-bar" class="hidden items-center gap-2 mb-2">
  <span class="text-[11px] text-gray-500">Showing <span id="filter-label" class="font-semibold text-gray-700"></span>
    — <span id="filter-count" class="font-semibold text-gray-700"></span> test(s)</span>
  <button onclick="filterTests('all')" class="text-[10px] px-2 py-0.5 rounded-md border border-gray-300 text-gray-600 hover:bg-gray-100">Show all</button>
</div>
<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-4">
  <div class="grid ${TEST_COLS} gap-2 px-3 py-1.5 text-[9px] font-semibold text-gray-400 uppercase tracking-wide bg-gray-50 border-b border-gray-100">
    <span>Test</span><span class="text-center">Status</span><span class="text-center">Duration</span>
  </div>
  <div id="no-match" class="hidden px-3 py-6 text-center text-[11px] text-gray-400">No tests with this status in this run.</div>
  <div class="flex flex-col gap-[1px]">
    ${ordered
      .map(function (t, i) {
        // A flaky test is reported as flaky and nothing else. It did end
        // green, but listing it under Passed too makes the tiles overlap and
        // stop adding up to the total.
        const cats = [t.flaky ? "flaky" : t.apiFailure ? "apifailed" : t.status]
          .concat(t.tags.map((x) => "tag-" + x))
          .concat(slowest.indexOf(t) !== -1 ? ["slowest"] : [])
          .concat(t.retries > 0 ? ["retried"] : [])
          .concat(t.retries > 0 && t.status === "passed" ? ["retrypassed"] : [])
          .concat(t.retries > 0 && t.status !== "passed" ? ["retryfailed"] : [])
          .join(" ");
        const testRecovered = t.status === "passed";
        const stepIcon = (s) =>
          s.status === "passed"
            ? '<span class="text-green-500">&#10003;</span>'
            : s.status === "skipped"
            ? '<span class="text-gray-300">&#8226;</span>'
            : testRecovered
            ? '<span class="text-amber-500">&#8635;</span>'
            : '<span class="text-red-500">&#10007;</span>';
        const VISIBLE_STEPS = 12;
        const stepRow = (s, hidden) =>
          `<div class="step-row-${i} ${hidden ? "hidden" : ""} grid grid-cols-[14px_1fr_50px] gap-2 items-start text-[11px] px-2 py-1 rounded ${
            s.status !== "passed" && s.status !== "skipped"
              ? (testRecovered ? "bg-amber-50" : "bg-red-50")
              : "bg-gray-50"
          }">
            <span class="${s.child ? "pl-2" : ""}">${stepIcon(s)}</span>
            <span class="${s.child ? "pl-3 text-gray-500" : ""} ${
            s.status === "passed"
              ? "text-gray-600"
              : testRecovered
              ? "text-amber-700"
              : "text-red-600 font-medium"
          }">${esc(s.name)}${
            s.status !== "passed" && s.status !== "skipped" && testRecovered
              ? '<span class="text-[9px] text-amber-600 bg-amber-100 rounded px-1 ml-1.5">handled — the test caught this and carried on</span>'
              : ""
          }</span>
            <span class="text-right text-gray-400">${secs(s.duration)}</span>
          </div>`;

        const hiddenCount = Math.max(0, t.steps.items.length - VISIBLE_STEPS);
        const stepsHtml = t.steps.items.length
          ? `<div class="text-[10px] font-semibold text-gray-400 uppercase tracking-wide mb-1">
              ${t.steps.kind === "named" ? "Steps" : "Playwright actions"}
              ${
                t.steps.kind === "actions"
                  ? '<span class="normal-case font-normal text-gray-400">— this test defines no named test.step() blocks, so this is the raw action trace</span>'
                  : ""
              }
            </div>
            <div class="flex flex-col gap-[2px] mb-2">
              ${t.steps.items.map((s, n) => stepRow(s, n >= VISIBLE_STEPS)).join("")}
              ${
                hiddenCount
                  ? `<button id="more-${i}" onclick="toggleSteps(event, ${i})"
                       class="text-[10px] text-blue-500 hover:text-blue-700 hover:underline px-2 py-1 text-left">
                       + ${hiddenCount} more step${hiddenCount > 1 ? "s" : ""}
                     </button>`
                  : ""
              }
            </div>`
          : '<div class="text-[11px] text-gray-400 mb-2">No step data recorded for this test.</div>';

        const shotHtml = t.screenshot
          ? `<div class="text-[10px] font-semibold text-gray-400 uppercase tracking-wide mt-2 mb-1">Screenshot at failure</div>
            <img src="${t.screenshot}" alt="Screenshot at the point of failure"
                 class="rounded-md border border-gray-200 max-w-full mb-2">`
          : "";

        const errorHtml = t.message || t.trace
          ? `<div class="text-[10px] font-semibold text-red-500 uppercase tracking-wide mb-1">Error</div>
            <div class="text-[11px] font-semibold text-gray-800 bg-red-50 border border-red-100 rounded-md px-2 py-1.5 mb-1.5">${esc(
              headline(t)
            )}</div>
            <details>
              <summary class="text-[10px] text-gray-400 cursor-pointer hover:text-gray-600">Full log</summary>
              <pre class="log-output text-[11px] bg-gray-50 border border-gray-200 rounded-md p-2 mt-1 overflow-auto whitespace-pre-wrap max-h-[260px]">${esc(
                t.message || t.trace
              )}</pre>
            </details>`
          : "";

        return `<div class="test-row border-b border-gray-50" data-cats="${cats}" data-slow-rank="${slowest.indexOf(t)}">
      <div class="grid ${TEST_COLS} gap-2 px-3 py-2 items-start cursor-pointer hover:bg-gray-50" onclick="toggleDetail(${i})">
        <span>
          <span class="text-[11px] leading-snug">
            <span class="text-gray-400 mr-1">&#9656;</span>
            ${
              splitTitle(t.name).context
                ? `<span class="text-[9px] text-gray-500 bg-gray-100 rounded px-1 py-0.5 mr-1 align-middle">${esc(
                    splitTitle(t.name).context
                  )}</span>`
                : ""
            }
            <span class="text-gray-800">${esc(splitTitle(t.name).intent)}</span>
          </span>
          <span class="block text-[9px] text-gray-400 mt-0.5 ml-3">
            ${esc(t.suite)}
            ${t.retries ? `<span class="text-amber-500 ml-1">&middot; ${t.retries} retr${t.retries > 1 ? "ies" : "y"}</span>` : ""}
          </span>
        </span>
        <span class="text-center">${statusPill(t)}</span>
        <span class="text-[11px] text-center text-gray-500">${secs(t.duration)}</span>
      </div>
      <div id="detail-${i}" class="hidden px-6 pb-3 bg-gray-50/50">
        <div class="flex flex-wrap gap-1 py-2">
          ${t.tags
            .map(
              (tag) =>
                `<span class="text-[9px] px-1.5 py-0.5 rounded bg-white border border-gray-200 text-gray-500">@${esc(tag)}</span>`
            )
            .join("")}
        </div>
        ${stepsHtml}
        ${errorHtml}
        ${shotHtml}
      </div>
    </div>`;
      })
      .join("")}
  </div>
  <div id="tests-pager" class="flex items-center justify-between gap-2 px-3 py-2 border-t border-gray-100 bg-gray-50"></div>
</div>`;

const miniTest = (t) =>
  `<div class="flex items-baseline gap-2 px-3 py-1 text-[11px] border-b border-gray-50 last:border-0">
    <span class="text-gray-700 flex-1">${esc(splitTitle(t.name).intent)}</span>
    <span class="text-[9px] text-gray-400">${esc(t.suite)}</span>
  </div>`;

// Only rendered when something is failing — on a green run these sections
// would be four empty boxes.
const triageHtml = failingNow.length
  ? `${section(
      "Failure Triage",
      previous
        ? "(compared against the previous run in this report's history)"
        : "(no previous run on record yet, so nothing can be compared)"
    )}
<div class="grid grid-cols-3 gap-3 mb-3">
  ${card("New failures", newFailures.length, newFailures.length ? "#EF4444" : "#9CA3AF")}
  ${card("Already failing", knownFailures.length, knownFailures.length ? "#F59E0B" : "#9CA3AF")}
  ${card("Not in previous run", unseenBefore.length, unseenBefore.length ? "#6B7280" : "#9CA3AF")}
</div>
${
  newFailures.length
    ? `<div class="bg-white border border-red-200 rounded-xl shadow-sm overflow-hidden mb-3">
    <div class="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wide bg-red-50 text-red-700 border-b border-red-100">
      New failures — were passing in the previous run
    </div>
    ${newFailures.map(miniTest).join("")}
  </div>`
    : ""
}
${
  knownFailures.length
    ? `<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-3">
    <div class="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wide bg-gray-50 text-gray-500 border-b border-gray-100">
      Already failing in the previous run
    </div>
    ${knownFailures.map(miniTest).join("")}
  </div>`
    : ""
}
${
  unseenBefore.length
    ? `<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-3">
    <div class="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wide bg-gray-50 text-gray-500 border-b border-gray-100">
      Not present in the previous run — no comparison possible
    </div>
    ${unseenBefore.map(miniTest).join("")}
  </div>`
    : ""
}
${section("Failures by Cause", "(" + failingNow.length + " failure(s) across " + causes.length + " distinct cause(s))")}
<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-4">
  ${causes
    .map(
      (c) => `<div class="border-b border-gray-50 last:border-0">
    <div class="flex items-center gap-2 px-3 py-2 bg-gray-50">
      <span class="text-[9px] font-bold text-white bg-red-500 rounded px-1.5 py-0.5">${c.count}</span>
      <span class="text-[11px] font-semibold text-gray-700">${esc(c.cause)}</span>
    </div>
    ${c.tests.map(miniTest).join("")}
  </div>`
    )
    .join("")}
</div>`
  : "";

const flakeHtml = repeatFlakes.length
  ? `${section("Flaky Frequency", "(how often each test flaked across the last " + history.length + " run(s) on record)")}
<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-4">
  <div class="grid grid-cols-[2.6fr_0.6fr] gap-2 px-3 py-1.5 text-[9px] font-semibold text-gray-400 uppercase tracking-wide bg-gray-50 border-b border-gray-100">
    <span>Test</span><span class="text-center">Flaked in</span>
  </div>
  ${repeatFlakes
    .map(
      (f) => `<div class="grid grid-cols-[2.6fr_0.6fr] gap-2 px-3 py-1.5 items-center border-b border-gray-50 last:border-0">
    <span class="text-[11px] text-gray-700">${esc(splitTitle(f.name).intent)}</span>
    <span class="text-[11px] text-center ${f.count > 1 ? "text-red-500 font-semibold" : "text-amber-500"}">
      ${f.count} of ${history.length}
    </span>
  </div>`
    )
    .join("")}
</div>`
  : "";

const MOD_COLS = "grid-cols-[1.8fr_0.5fr_0.4fr_0.4fr_0.4fr_0.4fr_0.5fr]";
const moduleHtml = `${section("Results by Module", "(grouped by spec folder — which product area is weakest)")}
<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-4">
  <div class="grid ${MOD_COLS} gap-2 px-3 py-1.5 text-[9px] font-semibold text-gray-400 uppercase tracking-wide bg-gray-50 border-b border-gray-100">
    <span>Module</span><span class="text-center">Pass Rate</span><span class="text-center">Total</span>
    <span class="text-center">Passed</span><span class="text-center">Failed</span>
    <span class="text-center">Flaky</span><span class="text-center">Avg Time</span>
  </div>
  ${modules
    .map(
      (m) => `<div class="grid ${MOD_COLS} gap-2 px-3 py-1.5 items-center border-b border-gray-50 last:border-0">
    <span class="text-[11px] font-medium text-gray-700">${esc(m.mod)}</span>
    <span class="text-[11px] font-semibold text-center" style="color:${sColor(m.rate)}">${m.rate.toFixed(1)}%</span>
    <span class="text-[11px] text-center font-semibold text-gray-700">${m.total}</span>
    <span class="text-[11px] text-center text-gray-600">${m.passed}</span>
    <span class="text-[11px] text-center ${m.failed ? "text-red-500 font-semibold" : "text-gray-400"}">${m.failed}</span>
    <span class="text-[11px] text-center ${m.flaky ? "text-amber-500 font-semibold" : "text-gray-400"}">${m.flaky}</span>
    <span class="text-[11px] text-center text-gray-500">${secs(m.avgMs)}</span>
  </div>`
    )
    .join("")}
</div>`;

const Q_COLS = "grid-cols-[2.6fr_1fr]";
const quarantineHtml = `${section(
  "Quarantined Tests",
  "(excluded from every run via --grep-invert @quarantine — these did NOT run)"
)}
<div class="bg-amber-50 border border-amber-200 rounded-xl px-3 py-2 mb-2 text-[11px] text-amber-800">
  These ${quarantined} tests are known-broken or intentionally disabled, so they are skipped before the run starts.
  They are not counted in the ${stats.total} above. Reasons are tracked in <span class="font-semibold">app-e2e-tests/TAGGING.md</span>.
</div>
<div class="bg-white border border-gray-100 rounded-xl shadow-sm overflow-hidden mb-4">
  <div class="grid ${Q_COLS} gap-2 px-3 py-1.5 text-[9px] font-semibold text-gray-400 uppercase tracking-wide bg-gray-50 border-b border-gray-100">
    <span>Test</span><span>Spec File</span>
  </div>
  <div class="flex flex-col gap-[1px]">
    ${quarantinedTests
      .map(
        (q) => `<div class="q-row grid ${Q_COLS} gap-2 px-3 py-1.5 items-center border-b border-gray-50">
      <span class="text-[11px] text-gray-700">${esc(q.name)}
        ${q.skipped ? '<span class="text-[9px] text-amber-600 bg-amber-50 border border-amber-200 rounded px-1 ml-1">test.skip() in source</span>' : ""}
      </span>
      <span class="text-[10px] text-gray-400">${esc(q.file)}</span>
    </div>`
      )
      .join("")}
  </div>
  <div id="q-pager" class="flex items-center justify-between px-3 py-2 border-t border-gray-100 bg-gray-50"></div>
</div>`;

const trendPoints = history.slice(-10);

// Three readings of the same runs: rate per run, what the run was made of,
// and the shape over time. Drawn by hand rather than pulling in a chart
// library — the report has to stay one self-contained file.
function trendBars() {
  return `<div class="flex items-end gap-3 h-[130px] px-2">
    ${trendPoints
      .map(
        (h) => `<div class="flex-1 flex flex-col items-center gap-1" style="max-width:90px">
      <span class="text-[9px] font-semibold" style="color:${sColor(h.passRate)}">${h.passRate.toFixed(0)}%</span>
      <div class="w-full rounded-t" style="height:${Math.max(2, h.passRate)}px;background:${sColor(h.passRate)}"></div>
      <span class="text-[8px] text-gray-400">${fmtDate(h.when)}</span>
    </div>`
      )
      .join("")}
  </div>`;
}

function trendStacked() {
  const maxTotal = Math.max.apply(
    null,
    trendPoints.map((h) => (h.passed || 0) + (h.failed || 0))
  ) || 1;
  return `<div class="flex items-end gap-3 h-[130px] px-2">
    ${trendPoints
      .map(function (h) {
        const clean = Math.max(0, (h.passed || 0) - (h.flaky || 0));
        const scale = 110 / maxTotal;
        const seg = (n, c) =>
          n > 0 ? `<div style="height:${Math.max(2, n * scale)}px;background:${c}" title="${n}"></div>` : "";
        return `<div class="flex-1 flex flex-col items-center gap-1" style="max-width:90px">
      <span class="text-[9px] text-gray-400">${(h.passed || 0) + (h.failed || 0)}</span>
      <div class="w-full flex flex-col-reverse rounded-t overflow-hidden">
        ${seg(clean, "#10B981")}${seg(h.flaky || 0, "#F59E0B")}${seg(h.failed || 0, "#EF4444")}
      </div>
      <span class="text-[8px] text-gray-400">${fmtDate(h.when)}</span>
    </div>`;
      })
      .join("")}
  </div>
  <div class="flex gap-4 px-2 pt-2">
    <span class="flex items-center gap-1 text-[10px] text-gray-500"><span class="w-2.5 h-2.5 rounded-sm" style="background:#10B981"></span>Passed</span>
    <span class="flex items-center gap-1 text-[10px] text-gray-500"><span class="w-2.5 h-2.5 rounded-sm" style="background:#F59E0B"></span>Flaky</span>
    <span class="flex items-center gap-1 text-[10px] text-gray-500"><span class="w-2.5 h-2.5 rounded-sm" style="background:#EF4444"></span>Failed</span>
  </div>`;
}

function trendLine() {
  const W = 900, H = 140, padX = 30, padY = 18;
  const n = trendPoints.length;
  const rates = trendPoints.map((h) => h.passRate);
  // A fixed 0-100 axis flattens the range that actually matters (a drop from
  // 98% to 94% is invisible), so the axis follows the data — with a minimum
  // 10-point window so noise is not magnified, and both bounds labelled so
  // the zoom is never misread.
  const lo = Math.max(0, Math.floor(Math.min.apply(null, rates) - 2));
  const hi = Math.min(100, Math.ceil(Math.max.apply(null, rates) + 2));
  const min = hi - lo < 10 ? Math.max(0, hi - 10) : lo;
  const span = hi - min || 1;
  const x = (i) => (n === 1 ? W / 2 : padX + (i * (W - padX * 2)) / (n - 1));
  const y = (v) => H - padY - ((v - min) / span) * (H - padY * 2);
  const ticks = [min, min + span / 2, hi];
  return `<div class="text-gray-400 px-2">
    <svg viewBox="0 0 ${W} ${H}" class="w-full" style="height:160px">
      ${ticks
        .map(
          (g) =>
            `<line x1="${padX}" x2="${W - padX}" y1="${y(g)}" y2="${y(g)}" stroke="currentColor" stroke-opacity="0.2" stroke-width="1"/>
             <text x="2" y="${y(g) + 3}" font-size="9" fill="currentColor">${g.toFixed(0)}%</text>`
        )
        .join("")}
      ${
        n > 1
          ? `<polyline points="${trendPoints.map((h, i) => x(i) + "," + y(h.passRate)).join(" ")}"
               fill="none" stroke="#3B82F6" stroke-width="2.5" vector-effect="non-scaling-stroke"/>`
          : ""
      }
      ${trendPoints
        .map(
          (h, i) =>
            `<circle cx="${x(i)}" cy="${y(h.passRate)}" r="4.5" fill="${sColor(h.passRate)}"><title>${h.passRate}%</title></circle>`
        )
        .join("")}
    </svg>
    <div class="flex justify-between px-1">
      ${trendPoints
        .map(
          (h) =>
            `<span class="text-[8px] text-gray-400">${fmtDate(h.when)} &middot; ${h.passRate.toFixed(1)}%</span>`
        )
        .join("")}
    </div>
    <div class="text-[9px] text-gray-400 px-1 pt-1">Axis zoomed to ${min.toFixed(0)}-${hi.toFixed(0)}% to show the change.</div>
  </div>`;
}

const trendBtn = (id, label) =>
  `<button onclick="showTrend('${id}')" id="trend-btn-${id}"
     class="trend-btn text-[10px] px-2 py-1 rounded border border-gray-300 text-gray-600 hover:bg-gray-100">${label}</button>`;

const trendHtml =
  trendPoints.length
    ? `${section("Trend", "(last " + trendPoints.length + " run" + (trendPoints.length > 1 ? "s" : "") + " on record)")}
<div class="bg-white border border-gray-100 rounded-xl p-3 shadow-sm mb-4">
  <div class="flex items-center gap-1.5 mb-3">
    ${trendBtn("bars", "Pass rate")}${trendBtn("stacked", "Composition")}${trendBtn("line", "Over time")}
    ${
      trendPoints.length < 2
        ? '<span class="ml-auto text-[10px] text-gray-400">Only one run so far — a trend needs at least two.</span>'
        : ""
    }
  </div>
  <div id="trend-bars">${trendBars()}</div>
  <div id="trend-stacked" class="hidden">${trendStacked()}</div>
  <div id="trend-line" class="hidden">${trendLine()}</div>
</div>`
    : `${section("Trend")}
<div class="bg-white border border-gray-100 rounded-xl p-6 shadow-sm mb-4 text-center text-[11px] text-gray-400">
  No runs on record yet — the trend appears once a run has been generated.
</div>`;

const html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>E2E QA Automation — Nudgebee</title>
<link rel="icon" type="image/svg+xml" href="${faviconUri}">
<script src="https://cdn.tailwindcss.com"><\/script>
<script>
tailwind.config = {
  theme: { extend: {
    colors: { navy: '#1B2D4A', nb: { yellow: '#FACF39', blue: '#3B82F6' } },
    fontFamily: { sans: ['Inter', 'Roboto', 'sans-serif'] }
  } }
}
<\/script>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<link href="https://fonts.googleapis.com/icon?family=Material+Icons+Outlined" rel="stylesheet">
<style>
  body { font-family: 'Inter', sans-serif; -webkit-font-smoothing: antialiased; }
  pre.log-output { font-family: 'SF Mono', Menlo, Consolas, monospace; }
  .nav-link { transition: all 0.2s; }
  .sidebar-wrapper { width: 220px; }
  .main-content-area { margin-left: 220px; }

  /* Dark theme, same override approach the benchmark report uses: recolour
     the Tailwind utilities rather than maintaining a second set of classes. */
  body.dark { background-color: #0b1220 !important; color: #e2e8f0; }
  body.dark .bg-white { background-color: #1e293b !important; }
  body.dark .bg-gray-50, body.dark .bg-gray-50\\/50 { background-color: #0f172a !important; }
  body.dark .bg-gray-100 { background-color: #334155 !important; }
  body.dark .text-gray-800, body.dark .text-gray-700 { color: #e2e8f0 !important; }
  body.dark .text-gray-600 { color: #cbd5e1 !important; }
  body.dark .text-gray-500 { color: #94a3b8 !important; }
  body.dark .text-gray-400 { color: #7c8aa0 !important; }
  body.dark .text-gray-300 { color: #64748b !important; }
  body.dark .border-gray-100,
  body.dark .border-gray-200,
  body.dark .border-gray-300,
  body.dark .border-gray-50 { border-color: #334155 !important; }
  body.dark .bg-red-50 { background-color: #3f1d1d !important; }
  body.dark .border-red-100 { border-color: #7f1d1d !important; }
  body.dark .bg-amber-50 { background-color: #3b2f11 !important; }
  body.dark .border-amber-200 { border-color: #92400e !important; }
  body.dark .text-amber-800 { color: #fcd34d !important; }
  body.dark .text-amber-600 { color: #fbbf24 !important; }
  body.dark .hover\\:bg-gray-50:hover { background-color: #172033 !important; }
  body.dark pre.log-output { background-color: #0f172a !important; color: #e2e8f0; }
  body.dark img { opacity: 0.92; }
</style>
</head>
<body class="bg-gray-50 text-gray-800 text-[13px]">
<div class="flex min-h-screen">
  <aside class="sidebar-wrapper bg-navy text-white flex flex-col flex-shrink-0 fixed h-full z-10 shadow-lg">
    <div class="text-center pt-6 pb-5 border-b border-white/10">
      <div class="mb-2">
        <svg width="32" height="30" viewBox="0 0 30 28" fill="none" xmlns="http://www.w3.org/2000/svg" class="inline-block">
          <path d="M9.8249 13.3367C9.8249 13.3367 9.35771 21.2788 14.6135 27.1186C14.6135 27.1186 8.90357 28.4767 6.7882 21.746C4.86107 15.6142 9.8249 13.3367 9.8249 13.3367Z" fill="#FACF39"/>
          <path d="M16.3256 12.2896C16.3256 12.2896 15.9186 19.2083 20.4972 24.2956C20.4972 24.2956 15.5229 25.4787 13.6802 19.6153C12.0013 14.2736 16.3256 12.2896 16.3256 12.2896Z" fill="#FACF39"/>
          <path d="M4.27727 18.3413C4.27727 18.3413 3.96108 23.7165 7.51819 27.6688C7.51819 27.6688 4.13111 27.8778 2.68576 23.8775C1.60879 20.8968 4.27727 18.3413 4.27727 18.3413Z" fill="#FACF39"/>
          <path d="M21.8266 10.2126C21.8266 10.2126 10.0886 -4.50372 2.02971 1.97845C-6.02921 8.46062 21.8266 10.2126 21.8266 10.2126Z" fill="#FACF39"/>
          <path d="M25.9048 19.5679C27.9146 19.5679 29.5439 17.9386 29.5439 15.9287C29.5439 13.9189 27.9146 12.2896 25.9048 12.2896C23.8949 12.2896 22.2656 13.9189 22.2656 15.9287C22.2656 17.9386 23.8949 19.5679 25.9048 19.5679Z" fill="#FACF39"/>
        </svg>
      </div>
      <div>
        <div class="text-nb-yellow font-bold text-[1.1rem] tracking-[1px] lowercase leading-tight">nudgebee</div>
        <div class="text-slate-400 text-[10px] tracking-wider">E2E QA Automation</div>
      </div>
    </div>
    <nav class="flex-1 flex flex-col text-[13px] mt-2 overflow-y-auto">
      <div class="px-5 pt-4 pb-1 text-[9px] font-semibold uppercase tracking-wider text-slate-500">Overview</div>
      <a href="#results" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#34D399">check_circle</span>
        <span>Results</span>
      </a>
      <a href="#duration" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#60A5FA">schedule</span>
        <span>Duration</span>
      </a>
      <a href="#trend" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#A78BFA">trending_up</span>
        <span>Trend</span>
      </a>
      <div class="px-5 pt-4 pb-1 text-[9px] font-semibold uppercase tracking-wider text-slate-500">Analysis</div>
      <a href="#triage" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#F87171">bug_report</span>
        <span>Failure Triage</span>
      </a>
      <a href="#modules" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#34D399">folder</span>
        <span>By Module</span>
      </a>
      <a href="#tags" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#FB923C">sell</span>
        <span>By Tag</span>
      </a>
      <div class="px-5 pt-4 pb-1 text-[9px] font-semibold uppercase tracking-wider text-slate-500">Tests</div>
      <a href="#tests" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#93C5FD">list</span>
        <span>Test Details</span>
      </a>
      <a href="#quarantine" class="nav-link flex items-center gap-2.5 px-5 py-2 border-l-[3px] border-transparent text-slate-400 hover:text-white hover:bg-white/10">
        <span class="material-icons-outlined text-[17px]" style="color:#FBBF24">block</span>
        <span>Quarantined</span>
        <span class="ml-auto text-[10px] bg-amber-400/20 text-amber-300 px-1.5 rounded">${quarantined}</span>
      </a>
    </nav>
    <button onclick="toggleTheme()"
            class="nav-link flex items-center gap-2.5 px-5 py-2.5 border-t border-white/10 text-slate-400 hover:text-white hover:bg-white/10 text-left text-[13px]">
      <span class="material-icons-outlined text-[18px]" id="theme-icon" style="color:#A78BFA">dark_mode</span>
      <span id="theme-label">Dark Mode</span>
    </button>
    <div class="px-5 py-4 border-t border-white/10 text-[10px] text-slate-500">
      ENV: ${esc(envName.toUpperCase())}
    </div>
  </aside>

  <main class="main-content-area flex-1 p-6">
    <div class="flex items-center gap-3 mb-4">
      <div class="w-9 h-9 rounded-lg bg-green-500/10 flex items-center justify-center">
        <span class="material-icons-outlined text-[20px]" style="color:#10B981">fact_check</span>
      </div>
      <div>
        <div class="text-[17px] font-semibold text-gray-800">E2E QA Automation</div>
        <div class="text-[11px] text-gray-500">Playwright suite results for the ${esc(envName)} environment</div>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <button onclick="downloadReport()"
                class="flex items-center gap-1.5 text-[11px] px-2.5 py-1.5 rounded-md border border-gray-300 bg-white text-gray-600 hover:bg-gray-100">
          <span class="material-icons-outlined text-[15px]">download</span> Download
        </button>
      </div>
      <div id="dl-note" class="hidden fixed top-4 right-4 z-50 text-[11px] bg-gray-900 text-white px-3 py-2 rounded-md shadow-lg max-w-xs"></div>
    </div>
    ${runContextHtml}
    ${headerHtml}
    <div id="results">${resultsHtml}</div>
    <div id="duration">${durationHtml}</div>
    <div id="triage">${triageHtml}</div>
    <div id="modules">${moduleHtml}</div>
    <div id="tags">${tagHtml}</div>
    <div id="flaky">${flakeHtml}</div>
    <div id="tests">${testHtml}</div>
    <div id="quarantine">${quarantineHtml}</div>
    <div id="trend">${trendHtml}</div>
  </main>
</div>
<script>
var REPORT = { env: ${JSON.stringify(envName)} };

function toast(msg) {
  var el = document.getElementById('dl-note');
  el.textContent = msg;
  el.classList.remove('hidden');
  setTimeout(function () { el.classList.add('hidden'); }, 6000);
}

async function downloadReport() {
  var name = 'e2e-qa-report-' + REPORT.env + '-' + new Date().toISOString().slice(0, 10) + '.html';
  // Serialises the page as it stands, so whatever filter or theme is active
  // is what the saved copy opens with.
  var html = '<!DOCTYPE html>' + String.fromCharCode(10) + document.documentElement.outerHTML;
  var blob = new Blob([html], { type: 'text/html' });

  // Inside the Claude artifact viewer a page cannot start its own download;
  // the downloads capability shows the viewer a save prompt instead. Absent
  // that (opened from disk, or hosted), the ordinary blob download works.
  if (window.claude && typeof window.claude.use === 'function') {
    try {
      var dl = await window.claude.use('downloads');
      if (dl) {
        await dl.save({ filename: name, data: blob });
        toast('Report saved.');
        return;
      }
    } catch (err) {
      toast(err && err.code === 'declined' ? 'Download cancelled.' : 'Could not save the report in this viewer.');
      return;
    }
  }

  try {
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    toast('Download started.');
  } catch (e) {
    toast('This viewer blocks downloads. Open the .html file directly to save a copy.');
  }
}

function applyTheme(dark) {
  document.body.classList.toggle('dark', dark);
  document.getElementById('theme-icon').textContent = dark ? 'light_mode' : 'dark_mode';
  document.getElementById('theme-label').textContent = dark ? 'Light Mode' : 'Dark Mode';
}

function toggleTheme() {
  var dark = !document.body.classList.contains('dark');
  applyTheme(dark);
  // Wrapped: the report is opened straight off disk and from sandboxed
  // viewers, where touching storage can throw outright.
  try { localStorage.setItem('nb-report-theme', dark ? 'dark' : 'light'); } catch (e) {}
}

(function () {
  // Light by default regardless of the OS setting — dark is opt-in via the
  // toggle, and the choice is remembered per viewer.
  var saved = null;
  try { saved = localStorage.getItem('nb-report-theme'); } catch (e) {}
  applyTheme(saved === 'dark');
})();

function showTrend(which) {
  ['bars', 'stacked', 'line'].forEach(function (id) {
    document.getElementById('trend-' + id).classList.toggle('hidden', id !== which);
    var btn = document.getElementById('trend-btn-' + id);
    btn.classList.toggle('bg-gray-100', id === which);
    btn.classList.toggle('font-semibold', id === which);
  });
}

function toggleDetail(i) {
  var el = document.getElementById('detail-' + i);
  if (el) el.classList.toggle('hidden');
}

function toggleSteps(ev, i) {
  // Stops the click from bubbling to the row, which would collapse the
  // whole panel the moment the extra steps appear.
  ev.stopPropagation();
  var rows = document.querySelectorAll('.step-row-' + i);
  var btn = document.getElementById('more-' + i);
  var expanding = btn.dataset.open !== '1';
  rows.forEach(function (r, n) {
    if (n >= 12) r.classList.toggle('hidden', !expanding);
  });
  btn.dataset.open = expanding ? '1' : '0';
  btn.textContent = expanding ? 'Show fewer steps' : '+ ' + (rows.length - 12) + ' more steps';
}

var ORIGINAL_ORDER = Array.prototype.slice.call(document.querySelectorAll('.test-row'));
var testPageSize = 15;
var qPageSize = 15;
var testFilter = 'all';
var testPage = 1;
var qPage = 1;

// Shared by both tables: rows-per-page on the left, page controls on the
// right. The size selector stays visible even on a single page, otherwise
// there is no way to switch back off a large page size.
function renderPager(el, page, total, size, pageCb, sizeCb) {
  var pages = Math.max(1, Math.ceil(total / size));
  var from = total ? (page - 1) * size + 1 : 0;
  var to = Math.min(page * size, total);
  var opts = [10, 15, 25, 50, 100]
    .map(function (n) {
      return '<option value="' + n + '"' + (n === size ? ' selected' : '') + '>' + n + '</option>';
    })
    .join('') + '<option value="0"' + (size >= 100000 ? ' selected' : '') + '>All</option>';

  var btn = function (label, target, disabled) {
    return '<button ' + (disabled ? 'disabled' : 'onclick="' + pageCb + '(' + target + ')"') +
      ' class="text-[10px] px-2 py-1 rounded border ' +
      (disabled ? 'border-gray-200 text-gray-300 cursor-not-allowed'
                : 'border-gray-300 text-gray-600 hover:bg-gray-100') + '">' + label + '</button>';
  };

  el.innerHTML =
    '<span class="flex items-center gap-1.5 text-[10px] text-gray-500">' +
      'Rows per page' +
      '<select onchange="' + sizeCb + '(this.value)" class="text-[10px] border border-gray-300 rounded px-1 py-0.5 bg-white text-gray-600">' +
        opts +
      '</select>' +
      '<span class="ml-2">Showing ' + from + '-' + to + ' of ' + total + '</span>' +
    '</span>' +
    '<span class="flex items-center gap-1">' +
      btn('First', 1, page <= 1) +
      btn('Prev', page - 1, page <= 1) +
      '<span class="text-[10px] text-gray-500 px-1">Page ' + page + ' of ' + pages + '</span>' +
      btn('Next', page + 1, page >= pages) +
      btn('Last', pages, page >= pages) +
    '</span>';
}

function setTestPageSize(v) {
  testPageSize = Number(v) === 0 ? 100000 : Number(v);
  testPage = 1;
  renderTests();
}

function setQPageSize(v) {
  qPageSize = Number(v) === 0 ? 100000 : Number(v);
  qPage = 1;
  renderQuarantine();
}

function renderTests() {
  // Order first, then read the DOM: the page slice is taken from live document
  // order, so reordering afterwards would page through the previous order.
  var all = Array.prototype.slice.call(document.querySelectorAll('.test-row'));
  var parent = all.length ? all[0].parentNode : null;
  if (parent) {
    if (testFilter === 'slowest') {
      all.slice()
        .sort(function (a, b) { return Number(a.dataset.slowRank) - Number(b.dataset.slowRank); })
        .forEach(function (r) { parent.appendChild(r); });
    } else {
      ORIGINAL_ORDER.forEach(function (r) { parent.appendChild(r); });
    }
  }

  var rows = document.querySelectorAll('.test-row');
  var matching = [];
  rows.forEach(function (row) {
    var match = testFilter === 'all' ||
      (' ' + row.dataset.cats + ' ').indexOf(' ' + testFilter + ' ') !== -1;
    if (match) matching.push(row);
    row.classList.add('hidden');
  });
  var pages = Math.max(1, Math.ceil(matching.length / testPageSize));
  if (testPage > pages) testPage = pages;
  matching.slice((testPage - 1) * testPageSize, testPage * testPageSize)
    .forEach(function (row) { row.classList.remove('hidden'); });

  var bar = document.getElementById('filter-bar');
  bar.classList.toggle('hidden', testFilter === 'all');
  bar.classList.toggle('flex', testFilter !== 'all');
  var LABELS = { retrypassed: 'retry passed', retryfailed: 'retry failed',
                 apifailed: 'API failures', slowest: 'slowest 10' };
  document.getElementById('filter-label').textContent =
    testFilter.indexOf('tag-') === 0 ? '@' + testFilter.slice(4)
      : (LABELS[testFilter] || testFilter);
  document.getElementById('filter-count').textContent = matching.length;
  document.getElementById('no-match').classList.toggle('hidden', matching.length > 0);
  renderPager(document.getElementById('tests-pager'), testPage, matching.length, testPageSize,
    'goTestPage', 'setTestPageSize');
}

function goTestPage(p) {
  testPage = p;
  renderTests();
  document.getElementById('tests').scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function filterTests(cat) {
  testFilter = cat;
  testPage = 1;
  renderTests();
  document.getElementById('tests').scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function renderQuarantine() {
  var rows = document.querySelectorAll('.q-row');
  rows.forEach(function (row, n) {
    row.classList.toggle('hidden', n < (qPage - 1) * qPageSize || n >= qPage * qPageSize);
  });
  renderPager(document.getElementById('q-pager'), qPage, rows.length, qPageSize,
    'goQPage', 'setQPageSize');
}

function goQPage(p) {
  qPage = p;
  renderQuarantine();
  document.getElementById('quarantine').scrollIntoView({ behavior: 'smooth', block: 'start' });
}

renderTests();
renderQuarantine();
showTrend('bars');
<\/script>
</body>
</html>`;

fs.writeFileSync(outFile, html);
console.log(
  `Wrote ${outFile} — ${stats.total} tests, ${stats.passed} passed, ${stats.failed + stats.broken} failed, ` +
    `${stats.flaky} flaky, ${quarantined} quarantined (excluded), ${history.length} run(s) in trend`
);
