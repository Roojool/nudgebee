# Code Analysis Eval Spec

This is the spec `llm/benchmark/swebench/README.md` refers to when it says the
log→code correlation gap is what `docs/code-analysis-eval-spec.md` exists for.
It lives here, next to the suite it specifies (`event_code_fix/`), rather than
under `swebench/`, since it defines a different suite's contract.

**Revision note:** this spec went through one `/challenge` pass. The first
draft assumed scenarios would be live, deliberately-broken k8s workloads whose
real output logs are picked up by Nudgebee's own organic metric/anomaly
detection (mirroring `nb_scenarios`). The adversarial review's strongest
objection was that this makes event *detection* — a fourth, never-explicitly-
scoped pipeline stage — a flaky, multi-minute dependency sitting in front of
the thing actually being measured. Following that review, the design changed
to **direct log injection**: scenarios are synthetic log/alert content posted
through a real, already-supported ingestion path (the `prometheus-alertmanager`
webhook), not live workloads whose failures must be organically noticed. This
removes the detection-timing risk almost entirely. The two other `/challenge`
findings (capability-resolution open question, fixture-repo drift) still apply
and are carried forward into §7.

## 1. What this measures (and what it explicitly does not)

**In scope:** whether the production event-analysis pipeline, given a real
event whose evidence is a synthetic-but-realistic log/alert bundle, (a) reaches
a correct root-cause diagnosis and (b) — for the two scenarios with a
corresponding code bug — produces a code-level fix that actually resolves it,
not just a diff that looks plausible.

This closes the gap `swebench/README.md` names verbatim: *"What it still does
not measure: log→code correlation. SWE-bench inputs are prose issues, not
telemetry."* `swebench/` proves the fixer can turn a correct diagnosis into a
correct patch, given a prose bug report handed to it directly. This suite
proves the pipeline can *produce* that correct diagnosis from log/telemetry
evidence in the first place, and that the automatic hand-off from diagnosis to
fix (which `swebench/` bypasses on purpose via `@code_analyzer`) actually
happens.

**v2 additions (this revision):** two more scenario *types* beyond the
original event/alert-driven ones — **explore mode** (`code_analyzer`'s
read-only Q&A path, queried directly rather than through the event pipeline)
and **PR follow-up** (`PRFollowupAgent`, driven against a real PR the harness
itself opens). See §4b/§4c, §5b/§5c, §6b/§6c.

**Out of scope, and why:**
- **Organic event detection** (anomaly/metric-threshold evaluation actually
  noticing a live failure) — deliberately bypassed per the revision above.
  Scenarios inject the event directly as log/alert content; whether Nudgebee's
  own detection layer would have noticed the same failure live is a real,
  separate question, for a later, explicitly-scoped track — not this suite.
- **Deterministic pre-LLM triage** (`api-server/services/triage`) — plain Go
  rule/scoring logic, no LLM involved. It has its own unit tests and a
  correlation replay harness (`api-server/services/triage/replay/`).

## 2. Key production mechanics this suite depends on

Verified directly against source:

- **Event creation, deterministic**: `POST /api/webhooks/prometheus-alertmanager`
  (registered `webhookGroup := r.Group("/api/webhooks")`,
  `api-server/services/api/public_webhooks.go:508,516`), authenticated by a
  per-integration token (`lookupIntegrationByToken`,
  `services/integrations/core/integration_webhook.go:375`). Payload is the
  standard Alertmanager webhook JSON:
  ```json
  {
    "alerts": [{
      "status": "firing",
      "labels": {"alertname": "...", "deployment": "...", "namespace": "...", "severity": "critical"},
      "annotations": {"summary": "...", "description": "<log lines go here>"},
      "startsAt": "2026-09-18T10:00:00Z", "endsAt": "...",
      "fingerprint": "...", "generatorURL": ""
    }],
    "externalURL": ""
  }
  ```
  Parsed by `services/integrations/prometheus_alertmanager_wehbook.go:ProcessEventWebook`
  (confirmed by direct read): `labels.deployment`/`.namespace` resolve the
  event's subject (`extractPromSubject`/`extractPromNamespace`),
  `annotations.description` becomes `EventDescription` — the text the
  investigation agent reads as evidence. This is a real, standard,
  already-supported ingestion path, not new benchmark-only code. Processing is
  queued (`webhook_queue.PublishWebhookProcess`) with a synchronous fallback if
  RabbitMQ is unavailable — either way, turnaround is webhook-processing
  latency (seconds), not a metric-evaluation cycle (minutes).
  **One-time setup precondition**: the benchmark account needs a
  `prometheus_alertmanager_webhook` integration configured once, giving the
  harness a webhook URL + token.
- **Diagnosis and fix run inside one job.** Entry point:
  `POST /v1/analyze/event/rca` (`llm/llm-server/api/event_analyzer.go:299`,
  on `groupV2 := r.Group("/v1/analyze")` at line 272), handling
  `EventRCAAnalysisRequest{EventId, AccountId, UserId, Regenerate, Generate}`
  (line 76), envelope `{"input": {"event_id": ..., "account_id": ..., "generate": true}}`
  (binds via `ActionRequest`, unwrapping `Input["request"]` if present — both
  forms accepted, `event_analyzer.go:302-317`). Poll-for-completion semantics:
  `executeEventRCAAnalysis`/`analyzeEventRCAUsingAgentsAndUpdateDb`
  (`api/rca_history.go:36`, `api/event_analyzer.go:1314`). If the investigation
  concludes there's a code-level cause, the **same job** automatically calls
  `code_analyzer` in fix mode, and the **same polled response** carries both
  halves: `EventAnalysisResponse.Analysis`/`.Investigation`/`.Summary`
  (diagnosis, lines 111-113) and `.GitDiff`/`.AutomatedFixPR` (fix, lines
  148/154).
- **Fix-stage gating** is explicit via `StatusReason` strings on
  `skipCodeFix(...)` (`event_analyzer.go:3491-3540`):
  `"skipped - investigation concluded no actionable code change"` (a genuine
  diagnosis-stage miss — scored as such), `"skipped - no git integration
  configured"` / `"skipped - no repository mapped for this workload"` (harness
  setup failures — never scored against the model).
- **Capability gating** comes from k8s workload annotations
  (`llm/llm-server/services_server/source_code.go:23-32`):
  `workloads.nudgebee.com/git.repo` / `.git.hash` for source fixes. Scenarios
  1 and 3 (below) need a lightweight Deployment object in-cluster carrying
  these annotations — it does **not** need to actually reproduce the bug live
  (the event is injected directly, not detected from this object's real
  behavior); it only needs to exist so capability resolution finds a repo for
  the subject the injected alert names. Scenario 2 is diagnosis-only and needs
  no such object.
  **Open question, not yet resolved by reading alone** —
  `ResolveEventCodeCapabilities` (`source_code.go:94`) also inspects
  `argocd.argoproj.io/tracking-id` and takes a `SourceCodeAnnotationOptions`
  parameter; whether an account-level repo mapping can override or supplement
  the annotations needs a closer read before scenarios 1 and 3 are authored
  against the two-annotation model assumed here (§7, blocking).
- **Event lookup** (to get the `event_id` the RCA call needs, after posting
  the synthetic alert) goes through the same query engine the events dashboard
  panel uses: `dashboards_execute_entity_query` action → api-server
  `/rpc/dashboard` → `services/dashboard/entity_query.go:ExecuteEntityQuery`,
  table `events_v2`, `query.QueryRequest{Table, Columns, Where}`
  (`services/query/entity_query.go:78`), `Where` a
  `{"_binary": {"<column>": {"_eq": <value>}}}` map (combine with `_and`),
  filtered on `fingerprint` (the value the harness itself chose when
  constructing the alert payload — see §3).
- **Direct `code_analyzer` entry** (explore mode and PR follow-up only,
  scenario types `explore`/`pr_followup` — not the `event` type above).
  Mirrors `swebench/run_swebench.py`'s own pattern exactly: POST
  `"@code_analyzer " + json.dumps({...})` as the `query` field to
  `POST /v1/completions/chat` (bypasses the router — a leading `@mention` is
  handled verbatim), then poll `POST /v1/completions/chat_get` with the
  returned `conversation_id` until terminal, then extract the `code_analyzer`
  agent's own JSON response from `llm_conversation_messages[].llm_conversation_agents[]`
  (`agent_name == "code_analyzer"`). This is a different entry point from the
  `event`-type scenarios' `/v1/analyze/event/rca` — no alert injection, no
  event lookup, because neither explore mode nor a follow-up call originates
  from an event in production either.
  - **Explore mode**: `{"query": ..., "git_repo": ..., "git_commit": ...,
    "mode": "explore"}`. Verified via direct read of
    `llm/code-analysis/agents/orchestrator_agent.go:31,416-420`: explore mode
    is read-only by contract — "the CodeFixerAgent never runs and no PR is
    created" — so this needs no GitHub write access at all. Response is
    `SubmitAnalysisInput`'s explore-mode contract
    (`llm/code-analysis/tools/submit_analysis.go:39-64`): `Answer` (prose,
    required), `Citations` (`[]Citation{FilePath, LineStart, LineEnd, Snippet,
    Note}`, at least one required), plus optional `Caveats`/
    `FollowUpSuggestions`.
  - **PR follow-up**: `{"followup": true, "pr_url": ..., "pr_branch": ...,
    "git_repo": ...}`. Verified via direct read of `agent_code2.go:3058-3156`
    (`executeFollowup`) and `pr_followup_agent.go`: this call is **fix-mode by
    definition** and requires a real, currently-open PR —
    `PRFollowupAgent.Execute` shells out to the real `gh pr view`/`gh pr
    comment` CLI (`gatherPRDetails`, `pr_followup_agent.go:1239-1250`) to read
    the PR's actual reviews, comments, and CI status; there is no injectable
    client and no way to feed it synthetic PR state through the request body.
    `AddressedComments` is explicitly documented as a **fallback only**
    (`pr_followup_agent.go:69-74`) — GitHub's own resolved-thread state is
    checked first and always wins. Concretely: **the harness must open a
    real PR and post a real comment or trigger a real failing check** before
    calling this — see §4c.

## 3. Scenario contract (schema)

Three scenario `type`s share one `scenario.yaml` convention, distinguished by
which top-level blocks they carry: `type: event` (the original three
scenarios — `alert` + optionally `workload`/`fixture_repo`), `type: explore`
(`explore` block, no alert/event involved), `type: pr_followup` (`pr_followup`
block, opens a real PR). See §4b/§4c for the schemas of the two new types.

```yaml
# scenarios/NNN_description/scenario.yaml
name: memory_leak_oom
tags: [code_fix, oom, memory_leak]

alert:
  alertname: PodOOMKilled
  labels: {deployment: subscriber-cache-worker, namespace: benchmark-scenarios, severity: critical}
  summary: "subscriber-cache-worker OOMKilled"
  log_lines: |
    2026-09-18T10:00:00Z level=info msg="heap_alloc_mb=180 goroutines=42"
    2026-09-18T10:05:00Z level=info msg="heap_alloc_mb=310 goroutines=44"
    2026-09-18T10:10:00Z level=warn msg="heap_alloc_mb=512 goroutines=210 gc_pause_ms=340"
    2026-09-18T10:12:00Z level=error msg="container OOMKilled: memory usage 1024Mi exceeded limit 1024Mi"
  # log_lines becomes annotations.description verbatim — this is the material
  # the investigation agent (the "log analyser") reads.
  fingerprint: bench-memory-leak-oom-001   # harness's own id, used to look the event back up

workload:                                  # omit entirely for a diagnosis-only scenario (see 002)
  manifests: manifests/all.yaml            # kubectl apply once; carries the git.repo/git.hash annotations
  namespace: benchmark-scenarios
  deployment: subscriber-cache-worker      # must match alert.labels.deployment

fixture_repo:                              # omit entirely for a diagnosis-only scenario
  url: https://github.com/nudgebee-org/nb-benchmark-fixtures
  subdir: memory_leak_oom                  # this repo hosts one scenario per top-level subdirectory
  bug_commit: a1b2c3d
  test_command: pytest -q
  fail_to_pass: [tests/test_cache.py::test_cache_evicts_stale_subscribers]
  pass_to_pass: [tests/test_cache.py::test_cache_returns_active_subscriber]

expected_diagnosis:
  - "The response must identify unbounded growth of an in-memory cache/map (no eviction or TTL) as the root cause, citing the rising heap_alloc_mb trend across the log lines."
  - "The response must NOT attribute this to an undersized memory limit — the pattern is gradual growth over time, not a one-off spike against a fixed ceiling."

expected_fix:                              # diagnostics only — never scored (see §6)
  file: memory_leak_oom/src/cache.py
  function: put
  description: "Cache entries are never evicted; add a TTL or bounded-size eviction policy."
```

`fail_to_pass`/`pass_to_pass` mirror SWE-bench's own field names: same trust
model — the scenario author verifies once, at authoring time, that
FAIL_TO_PASS fails at `bug_commit` and PASS_TO_PASS is green there. The harness
re-verifies this at the start of every run anyway (§6, `fixture_drift`), since
unlike SWE-bench's static published tasks, this fixture repo is a live,
writable repo the team itself can accidentally drift.

**Fixture repo hosting**: one small dedicated external repo
(`nudgebee-org/nb-benchmark-fixtures`) shared across all code-fix scenarios,
one top-level subdirectory per scenario (`subdir`), one bug per commit within
that subdirectory — not a monorepo subdirectory of `nudgebee-enterprise`
itself, so the annotation contract has a real clonable URL + commit SHA
without entangling the disposable-clone-and-test step with the main repo's own
checkout/credentials path. `code_fix_verifier.py` always runs `git apply` at
the repo root (diff paths are relative to it) but scopes test execution to
`subdir`, so a repo-wide test command doesn't also pick up other scenarios'
fixtures.

## 4. The three scenarios

### 001 — memory leak → OOMKilled (code fix)
Log lines show heap usage climbing steadily over ~12 minutes before the kill —
the *shape* of the numbers, not just the OOMKilled event, is the diagnostic
signal. Root cause: an in-memory cache/map with no eviction (`src/cache.py`).
Deliberately paired with 002 (same OOMKilled symptom, different cause, opposite
shape) to test whether diagnosis distinguishes them rather than pattern-
matching "OOMKilled → say memory leak" regardless of evidence.

### 002 — undersized resource limit → OOMKilled (diagnosis only)
Log lines show **flat, stable** memory usage right up to a single abrupt spike
during a large one-off batch job, then the kill — no gradual climb. Root
cause: the configured memory limit is undersized for a legitimate (non-leaky)
workload spike, e.g. one log line noting `resources.limits.memory=256Mi` while
processing a `size=50000` row batch. No `workload`/`fixture_repo` block: there
is no source-code bug here, the correct "fix" is a deployment/values change
(bump the limit), which has no natural test suite to verify against — scored
on diagnosis correctness only. Expected diagnosis must explicitly reject the
"memory leak" explanation given the flat-then-spike shape, mirroring the
OpenEnv Incident-Triage-Environment's negation-aware scoring (penalize a
plausible-sounding wrong answer, not just a missing right one).

### 003 — badly-optimized query → high API latency (code fix)
Log lines show a specific endpoint (`/api/orders/by-email`) repeatedly slow
(multi-second `slow query` / `request completed ... latency_ms=...` lines).
Root cause: an N+1 query pattern in the request handler (fetch orders, then
loop and fetch customer info one row at a time) instead of a single join —
chosen over a missing-index bug because "number of DB calls per request" is a
robust, deterministic unit-test assertion (mocked DB call counter), whereas
asserting real query *latency* in a unit test is not. FAIL_TO_PASS asserts the
call count for one request is ≤ 2; it's N+1 (fails) at `bug_commit`, 1 (passes)
after the fix.

### 004 — synchronous startup work blocks the health server → CrashLoopBackOff (code fix)
Mined from a real, merged fix in this repo (PR #37133, account `k8s-dev`, the
same internal test account used throughout this repo's own test suites — no
anonymization needed). Log lines show repeated readiness-probe failures
(`dial tcp <pod-ip>:8000: connect: connection refused`) followed by SIGTERM
(exit 137) in a tight loop — the *shape* is "never came up," not "came up
then crashed," which is the signal that startup itself is blocked, not a
runtime fault. Root cause: a slow synchronous startup task (schedule sync)
runs before the HTTP health server starts listening, so a readiness-probe
deadline shorter than the sync always fails. Real fix: background the sync in
a goroutine so the health server binds immediately. Deliberately a different
failure class from 001/002/003 — startup-ordering, not memory or query
behavior.

### 005 — no fallback when a dependency is unreachable at boot → restart loop (code fix)
Mined from a real, merged fix (PR #38409, same internal account). Log lines
show a Redis connection refusal at boot followed by immediate process exit,
repeating on every restart — contrasted with other services staying healthy,
signaling a code-level resilience gap rather than an actual Redis outage.
Root cause: a rate limiter treats a Redis-connect failure as fatal instead of
falling back to an in-memory store. Real fix: catch the error, fall back.
Deliberately a different failure class again — missing graceful degradation
on an external dependency, not a bug in the service's own logic.

## 4b. Explore-mode scenarios

No alert, no event, no fixture-repo test verification — a direct
natural-language question to `code_analyzer` in explore mode, pointed at the
**same fixture repos** 001 and 003 already use (no new fixtures needed).
Scored on whether the answer is correct *and* whether its citation actually
names the real buggy file — a deterministic check layered on top of the
existing LLM-judge diagnosis scoring.

```yaml
# scenarios/006_explore_memory_leak/scenario.yaml
name: explore_memory_leak
type: explore
tags: [explore, oom, memory_leak]

explore:
  query: "Why might subscriber-cache-worker's memory grow over time? Look at the cache implementation."
  git_repo: https://github.com/nudgebee-org/nb-benchmark-fixtures
  git_commit: REPLACE_WITH_BUG_COMMIT_SHA   # same commit as scenario 001

expected_diagnosis:
  - "The response must identify that put() never evicts entries, so the cache grows without bound."

expected_citation:
  file_contains: memory_leak_oom/src/cache.py   # substring match against Citations[].FilePath
```

Two scenarios: `006_explore_memory_leak` (reuses 001's fixture/commit) and
`007_explore_n_plus_one_query` (reuses 003's fixture/commit, asking "why
might /api/orders/by-email be slow? Look at how orders are fetched.").

## 4c. PR follow-up scenario

The one scenario type with real GitHub side effects on every run. The harness
plays the role production would: it opens a plausible-but-incomplete PR
itself (via `gh pr create`, **not** via the production pipeline's
`raise_pr` — a different code path, so this doesn't reopen the "no real PR"
question the fix-verification scenarios settled), posts one real review
comment describing a concrete gap, then drives `PRFollowupAgent` and checks
whether the gap actually got closed.

**This tests the top-level-issue-comment trigger path specifically** — not
inline review comments or CI-failure state, which are `PRFollowupAgent`'s
other, more central inputs (`gatherInlineComments`, `gatherCIFailureLogs`).
A plain `gh pr comment` was chosen because it needs no diff-position
plumbing to fabricate and `PRFollowupAgent` does read issue comments
(`gatherIssueComments`), but per `/challenge`'s counterargument 3: don't
read this scenario's score as "PR-followup quality" in general — it's one
specific, possibly-easier-or-harder-than-average trigger shape, the same
"record the entry agent" discipline `swebench/README.md` already applies to
itself. `tags` below carry `trigger:issue_comment` so this is unambiguous in
any report.

```yaml
# scenarios/008_pr_followup_cache_size_limit/scenario.yaml
name: pr_followup_cache_size_limit
type: pr_followup
tags: [pr_followup, "trigger:issue_comment"]

pr_followup:
  fixture_repo:
    url: https://github.com/nudgebee-org/nb-benchmark-fixtures
    subdir: memory_leak_oom
    base_commit: REPLACE_WITH_BUG_COMMIT_SHA
    test_command: pytest -q
    # The ORIGINAL scenario's own checks — re-run against the followup
    # commit too (not just fail_to_pass_after_followup below), so closing
    # the review comment can't be credited if it silently reopened this.
    fail_to_pass: [tests/test_cache.py::test_cache_evicts_stale_subscribers]
    pass_to_pass: [tests/test_cache.py::test_cache_returns_active_subscriber]
  # A real diff (checked in alongside scenario.yaml, verified locally): fixes
  # the eviction bug with a hardcoded FIFO cap, ignores an env-configurable
  # limit — and already includes the (currently failing)
  # fail_to_pass_after_followup test below, so the agent's job is to make an
  # EXISTING named test pass, the same trust model every other fail_to_pass
  # in this suite already uses, not to invent its own test name from prose.
  incomplete_fix_diff_file: incomplete_fix.patch
  review_comment: >
    This caps the cache at a fixed size, but we need the limit configurable
    via a MAX_CACHE_SIZE env var (default 1000) — see the failing
    test_cache_respects_max_cache_size_env_var test, which should pass once
    that's wired up.
  fail_to_pass_after_followup:
    - tests/test_cache.py::test_cache_respects_max_cache_size_env_var
```

## 5. Execution contract

1. **One-time setup** (per cluster/account, not per run): a
   `prometheus_alertmanager_webhook` integration configured on the benchmark
   account (gives the harness a webhook URL + token); for scenarios 1 and 3,
   `kubectl apply -f scenarios/00N_.../manifests/all.yaml` (a lightweight,
   annotated Deployment — persistent, applied once, not per-run); an enabled
   git integration on the benchmark account (`isGitIntegrationConfigured`).
2. **Per run**: POST the scenario's Alertmanager-shaped payload (§3) to the
   webhook URL. Poll `events_v2` (via `dashboards_execute_entity_query`)
   filtered on the scenario's `fingerprint`, bounded by a short timeout (this
   is webhook-processing latency, expected in seconds — a long wait here means
   something is actually broken in ingestion, not "detection hasn't happened
   yet organically," and is bucketed `ingestion_timeout`, a rare/infra bucket,
   not a per-scenario tuning knob).
3. `POST /v1/analyze/event/rca {event_id, account_id, generate: true}`, poll
   until `status` terminal, per `executeEventRCAAnalysis`'s contract.
4. `raise_pr` is **not** set by the harness — `EVENT_AUTO_RAISE_PR_ENABLED`
   already defaults off in production, so diff-only is free, not an override.

## 5b. Explore-mode execution contract

1. `"@code_analyzer " + json.dumps({"query", "git_repo", "git_commit", "mode": "explore"})`
   to `/v1/completions/chat`, poll `/v1/completions/chat_get` — same
   submit/poll/extract mechanic as `swebench/run_swebench.py`'s `Nubi` class.
2. No alert, no event, no fixture-repo clone, no GitHub write access. This is
   the cheapest scenario type in the suite.

## 5c. PR follow-up execution contract

1. **Setup** (per run, not one-time — each run needs its own fresh PR so
   scoring isn't contaminated by a previous run's state): clone the fixture
   repo at `base_commit`, apply `incomplete_fix_diff_file`, push a
   **namespaced** branch (`event-code-fix/pr-followup-<run-id>-<timestamp>`,
   never a bare/reused name — see the cleanup note below), `gh pr create`
   against `nudgebee-org/nb-benchmark-fixtures`. Post `review_comment` via
   `gh pr comment` (a real top-level PR comment — a real *inline review*
   comment would need a diff position; a plain issue-style comment on the PR
   is sufficient input, since `PRFollowupAgent` reads issue comments too, per
   `gatherIssueComments`).
2. **Drive**: `"@code_analyzer " + json.dumps({"followup": true, "pr_url": ..., "pr_branch": ..., "git_repo": ...})`
   via the same direct-entry mechanic as explore mode (§2, §5b).
3. **Verify**: re-fetch the PR's real state — a new commit on the branch
   (compare HEAD SHA before/after), then clone that new HEAD and run
   **both** `fail_to_pass_after_followup` (the new gap must now be closed)
   **and** the scenario's original `fail_to_pass`/`pass_to_pass` from
   `fixture_repo` (§3's `event`-type fields, reused here) — per
   `/challenge`'s counterargument 2, closing the reviewer's gap must not be
   allowed to silently reopen the bug the PR was originally meant to fix.
4. **Clean up, crash-resilient, not just happy-path**: close the PR
   (`gh pr close`) after scoring regardless of outcome. Because a crashed or
   interrupted run can still leave one open, the harness also sweeps
   (`gh pr list --search "head:event-code-fix/pr-followup-"`, filtered to
   anything older than a few hours) and closes orphans at the **start** of
   every run, not just at the end of its own — one crashed run's litter gets
   swept by the next one, so nothing needs a separate cron just for cleanup.

## 6. Scoring contract

- **Diagnosis**: RAGAS/verdict-judge (`llm/agents/common/ragas_evaluation.py`,
  `verdict_judge.py` — the same machinery `nb_scenarios` uses) against
  `expected_diagnosis` bullets. A 0-100 score plus a qualitative verdict, not a
  hard pass/fail.
- **Code fix** (scenarios 1 and 3 only): **binary**, deterministic.
  Before scoring, re-verify `fail_to_pass` still fails against an unpatched
  clone at `bug_commit` (the `fixture_drift` check, §7) — if it doesn't, abort
  that run's fix-scoring into a distinct `fixture_drift` bucket rather than
  silently scoring against a corrupted baseline. Otherwise: clone, `git apply`
  the RCA response's `GitDiff`, run `fail_to_pass` (must now pass), then
  `pass_to_pass` (must stay green). **Not** diff-exact-match —
  `swebench/README.md` warns `diff_exact` scored 0.077 on a run that actually
  resolved 50%, because a correct fix written differently scores zero.
- **Buckets**:

  | Bucket | Meaning |
  |---|---|
  | `resolved` | FAIL_TO_PASS passes, PASS_TO_PASS clean |
  | `unresolved` | patch applied, tests still fail |
  | `empty_patch` | fix stage ran but produced no diff |
  | `error` | patch didn't apply, or the test harness itself errored |
  | `fixture_drift` | FAIL_TO_PASS didn't fail at `bug_commit` before any patch was applied — a harness/fixture integrity problem, not scored against the model |
  | `skip:<reason>` | fix stage never ran. Split into **setup failures** (`no_git_integration`, `no_repository_mapped` — not scored) and **model misses** (`no_actionable_code_change` — scored as a diagnosis failure) |
  | `ingestion_timeout` | the injected alert never produced a queryable event within the (short) timeout — an infra/harness issue, not scored as either diagnosis or fix |

- **Metrics captured**: tokens/cost for the **diagnosis** stage
  (`llm/agents/common/metrics.py:get_token_metrics`, keyed by
  `"event-rca-" + response.event_fingerprint` — the same session-id
  construction `event_analyzer.go:1445` uses), plus wall-clock per stage. The
  **fix** stage's `CodeAgent2` conversation runs under a separate session not
  exposed on `EventAnalysisResponse` — its tool-call/token metrics are a known
  gap, not wired in v1 rather than guessed at.

## 6b. Explore-mode scoring contract

- **Answer quality**: RAGAS/verdict-judge against `expected_diagnosis`
  bullets — same machinery, same reasoning as event-type diagnosis scoring.
- **Citation grounding** (new, deterministic): at least one entry in
  `Citations[].FilePath` must contain `expected_citation.file_contains`. This
  is cheap and catches a specific failure mode LLM-judge scoring alone can
  miss — a plausible-sounding answer that doesn't actually point at the real
  file, i.e. a good guess rather than a grounded finding.
- Buckets: `resolved` (both checks pass), `partial` (answer correct, citation
  missing/wrong, or vice versa), `failed`, `error`.

## 6c. PR follow-up scoring contract

- **Binary, deterministic** — same trust model as code-fix scoring (§6):
  `resolved` only if a new commit landed on the PR branch AND
  `fail_to_pass_after_followup` passes against that new HEAD **AND** the
  original scenario's own `fail_to_pass`/`pass_to_pass` (§6) still hold
  against that same new HEAD. A regression on the original fix is
  `unresolved`, exactly like a regression in the fix-mode scenarios (§6) —
  addressing the reviewer's comment is not credited if it broke the thing
  the PR was for.
- `no_op` — a distinct bucket from `unresolved`: `PRFollowupResult.NoOp`
  (`pr_followup_agent.go`) means the agent itself concluded nothing needed
  addressing. If the review comment named a real, verifiable gap and the
  agent still reports `NoOp`, that's a genuine model miss, not a harness
  problem — but it's bucketed separately from `unresolved` (agent tried and
  didn't converge) so the two failure modes aren't averaged together.
- `setup_error` — the harness's own PR/branch/comment setup failed (e.g. `gh
  pr create` rejected) — never scored against the model.

## 7. `/challenge` findings carried into this design

1. **Detection-timing flakiness (fatal-ish in the first draft)** — resolved by
   switching to direct alert injection (§2, §5). Residual risk is
   `ingestion_timeout`, expected to be rare and infra-only, not a model-quality
   signal — bucketed separately, never silently folded into a score.
2. **`ResolveEventCodeCapabilities` open question — still blocking.** Read
   `source_code.go`'s full annotation-resolution path (ArgoCD tracking-id
   branch, `SourceCodeAnnotationOptions`) and do one real dry run against
   scenario 001's manifest before authoring scenario 003 the same way.
3. **Fixture-repo drift — mitigated by the `fixture_drift` bucket (§6),
   checked on every run**, not just at authoring time, since this fixture repo
   (unlike SWE-bench's static published tasks) is a live, writable repo the
   team itself can accidentally modify.

A second `/challenge` pass ran specifically on the PR-followup addition
(§4c/§5c/§6c) before it was built, since it's the one scenario type with real,
repeated external side effects and no dry-run alternative (`PRFollowupAgent`
shells out to real `gh pr view`/`gh pr comment`, confirmed by reading the
code — no injectable mock exists). Verdict: **REVISE**, three changes applied
before implementation:

4. **No cleanup story for a crashed run** — fixed by namespacing every PR-
   followup branch (`event-code-fix/pr-followup-<run-id>-<timestamp>`) and
   sweeping orphans older than a few hours at the **start** of every run
   (§5c), not just closing happy-path at the end.
5. **Followup scoring dropped the original fix's own regression check** —
   fixed by re-running the scenario's own `fail_to_pass`/`pass_to_pass`
   alongside the new gap's test when verifying the followup commit (§5c/§6c).
6. **The synthetic top-level-comment trigger may not represent
   `PRFollowupAgent`'s primary (inline-comment / CI-failure) input paths** —
   not fixable without real CI infrastructure on the fixture repo (deferred,
   §10), so instead made unmissable: `trigger:issue_comment` tag (§4c) so no
   future report over-generalizes this scenario's score into "PR-followup
   quality" broadly, mirroring `swebench/README.md`'s own "record the entry
   agent" discipline.

## 8. Reporting

CLI + JSON report only (`event_code_fix_results.json`, resumable — same
pattern as `swebench_results.json`). Not wired into `benchmark_server`/the
dashboard — that auto-discovers suites via `config.yaml` under `llm/agents/`,
built around a one-shot prompt→answer pytest engine this suite's multi-stage
async+clone+test-run shape doesn't fit. Never run in CI — manual or scheduled
Job only (`event_code_fix/k8s/`).

## 9. Acceptance criteria

Deliberately modest, for a 3-scenario v1 meant to prove the harness works end
to end — not a claim about what the pipeline *should* score:

- **Diagnosis**: 002 (the negation case) must not score a `failed` verdict —
  it is the sharpest test of whether the diagnosis is evidence-driven rather
  than pattern-matching the alert name. 001 and 003 should each reach at least
  `partially_correct`.
- **Code fix**: 001 and 003 should not both land in the same bucket — seeing
  real discrimination (e.g. one `resolved`, one `unresolved`) is more valuable
  for proving the harness works than either scenario's specific result.

Report the numbers; don't gate on them (the `nb_scenarios`/`swebench`
precedent) — compare only same-suite runs against each other.

**Explore mode (006/007)**: both should reach `resolved` — these are the same
two bugs the fix-mode scenarios already diagnose correctly-or-not; explore
mode answering a direct question about a known bug is a lower bar than
diagnosing it from telemetry, so a `failed` here would be a red flag about
citation-grounding specifically, not diagnosis quality in general.

**PR follow-up (008)**: report the bucket; no threshold set yet — this is the
first run of a scenario type this suite has never exercised before,
so establishing *a* number matters more than clearing a bar. Revisit once a
baseline exists.

## 10. Known gaps / v2

**Organic detection track**: a later, explicitly separate suite could test
whether Nudgebee's own anomaly/metric-threshold detection notices these bug
classes live, without the alert being handed to it — a genuinely different
question from log→code correlation, deliberately not conflated with it here.

**PR follow-up, iteration beyond one round**: scenario 008 scores a single
review-comment round. A harder version would inject a second round (the
agent's first attempt still misses something) to test whether it converges
over multiple iterations rather than just one — not designed here.

**CI-failure-triggered follow-up**: 008 uses a review comment, not a failing
CI check, since it needs no CI workflow set up on the fixture repo at all.
Adding a CI-failure variant would need a real GitHub Actions workflow in
`nb-benchmark-fixtures` that can be made to fail on demand — extra fixture-repo
setup, deliberately deferred.
