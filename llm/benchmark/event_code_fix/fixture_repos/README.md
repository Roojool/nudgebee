# Fixture repo staging

`memory_leak_oom/`, `n_plus_one_query_latency/`, `startup_ordering_crashloop/`,
and `redis_fallback_crashloop/` here are **staged content** for the real
fixture repo the scenarios point at
(`https://github.com/nudgebee-org/nb-benchmark-fixtures`), not the repo
itself. Creating an external GitHub repo is a real, shared action outside
this monorepo, so it isn't done automatically — a human does this once:

```bash
gh repo create nudgebee-org/nb-benchmark-fixtures --private --description "event_code_fix benchmark fixtures"
git clone https://github.com/nudgebee-org/nb-benchmark-fixtures /tmp/nb-benchmark-fixtures
cp -r fixture_repos/memory_leak_oom /tmp/nb-benchmark-fixtures/
cp -r fixture_repos/n_plus_one_query_latency /tmp/nb-benchmark-fixtures/
cp -r fixture_repos/startup_ordering_crashloop /tmp/nb-benchmark-fixtures/
cp -r fixture_repos/redis_fallback_crashloop /tmp/nb-benchmark-fixtures/
cd /tmp/nb-benchmark-fixtures
git add memory_leak_oom n_plus_one_query_latency startup_ordering_crashloop redis_fallback_crashloop
git commit -m "add event_code_fix benchmark fixtures"
git push
git rev-parse HEAD   # this is the bug_commit for ALL FOUR scenarios (one commit added all four)
```

Then update, in this repo, each scenario's `fixture_repo.bug_commit` (or
`pr_followup.fixture_repo.base_commit` for 008) and matching
`workloads.nudgebee.com/git.hash` manifest annotation to the SHA above:
- `scenarios/001_memory_leak_oom/{scenario.yaml,manifests/all.yaml}`
- `scenarios/003_n_plus_one_query_latency/{scenario.yaml,manifests/all.yaml}`
- `scenarios/004_startup_ordering_crashloop/{scenario.yaml,manifests/all.yaml}`
- `scenarios/005_redis_fallback_crashloop/{scenario.yaml,manifests/all.yaml}`
- `scenarios/006_explore_memory_leak/scenario.yaml`'s `explore.git_commit`
  (reuses 001's fixture — no manifest, no annotation, explore mode is
  read-only)
- `scenarios/007_explore_n_plus_one_query/scenario.yaml`'s `explore.git_commit`
  (reuses 003's fixture, same reasoning)
- `scenarios/008_pr_followup_cache_size_limit/scenario.yaml`'s
  `pr_followup.fixture_repo.base_commit` (reuses 001's fixture — no
  manifest either; this scenario opens its own disposable PR/branch per run,
  it doesn't need a standing annotated workload)

Verify each scenario's `fail_to_pass`/`fail_to_pass_after_followup` genuinely
fails and `pass_to_pass` genuinely passes at that commit before relying on
it — `code_fix_verifier.py`/`pr_followup_verifier.py` re-check this on every
run (`fixture_drift` for the event-type scenarios; the followup scenario's
own re-check of the original `fail_to_pass`/`pass_to_pass` serves the same
purpose) but a human should confirm it once at authoring time too. All four
staged fixtures, plus scenario 008's `incomplete_fix.patch`, were verified
locally before being committed here — bug version: FAIL_TO_PASS fails,
PASS_TO_PASS passes; a plausible fix: FAIL_TO_PASS also passes; for 008
specifically, the incomplete-fix state passes the original two tests but
fails the new env-var test, and a real completion passes all three.

**Scenario 008's `incomplete_fix.patch`** is a real diff generated against
`memory_leak_oom`'s bug commit (paths are `memory_leak_oom/src/cache.py` and
`memory_leak_oom/tests/test_cache.py`, matching this repo's one-subdirectory-
per-scenario layout) — it must be regenerated if `memory_leak_oom/src/cache.py`
ever changes, since a stale patch will fail to apply cleanly (bucketed
`setup_error`, not scored against the model, but still a broken scenario
until fixed).

If another code-fix scenario is added later, add its own top-level
subdirectory the same way (`subdir: <scenario_name>` in its `scenario.yaml`)
rather than a new repo — see docs/code-analysis-eval-spec.md's "Fixture repo
hosting" note for why this is one shared repo, not one repo per scenario.
