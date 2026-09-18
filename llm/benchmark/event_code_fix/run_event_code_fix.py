#!/usr/bin/env python3
"""Score the production event-analysis + code-fix pipeline against synthetic,
log-driven scenarios. See docs/code-analysis-eval-spec.md for the full
contract; README.md for setup and usage.

Three scenario types, dispatched by each scenario.yaml's `type` field
(default "event" for backward compatibility with the original three):
  event       — alert injection -> diagnosis -> optional code fix
  explore     — direct, read-only code_analyzer query, no alert involved
  pr_followup — harness opens a real PR, drives PRFollowupAgent, tears down

Usage:
    export API_SERVER_URL=... ACTION_API_SERVER_TOKEN=... ALERTMANAGER_WEBHOOK_URL=...
    export LLM_SERVER_URL=... LLM_SERVER_TOKEN=...
    export BENCHMARK_ACCOUNT_ID=... BENCHMARK_TENANT_ID=...
    python run_event_code_fix.py [--scenario NAME ...] [--diagnosis-only]
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

import event_rca_client
import pr_followup_verifier
from code_analyzer_client import CodeAnalyzerClient
from code_fix_verifier import verify_fix
from diagnosis_scorer import diagnosis_stage_metrics, score_diagnosis
from event_rca_client import EventCodeFixClient, IngestionTimeout
from explore_scorer import score_explore
from scenario_loader import load_scenarios

SETUP_SKIP_REASONS = {
    "skipped - no git integration configured": "skip:no_git_integration",
    "skipped - no repository mapped for this workload": "skip:no_repository_mapped",
}
MODEL_MISS_SKIP_REASONS = {
    "skipped - investigation concluded no actionable code change": "skip:no_actionable_code_change",
}


def run_event_scenario(
    client: EventCodeFixClient, scenario: dict, diagnosis_only: bool
) -> dict:
    row = {"name": scenario["name"], "tags": scenario.get("tags", [])}
    t0 = time.monotonic()

    client.post_alert(scenario)
    try:
        event_id = client.wait_for_event(scenario["alert"]["fingerprint"])
    except IngestionTimeout as exc:
        row["bucket"] = "ingestion_timeout"
        row["detail"] = str(exc)
        row["seconds"] = round(time.monotonic() - t0, 1)
        return row

    rca = client.run_rca(event_id)
    row["diagnosis_seconds"] = round(time.monotonic() - t0, 1)

    if not isinstance(rca, dict):
        row["bucket"] = "error:rca_invalid_payload"
        row["detail"] = "RCA response payload is not a dictionary"
        row["seconds"] = round(time.monotonic() - t0, 1)
        return row

    rca_status = str(rca.get("status", "")).lower()
    if rca_status not in event_rca_client.TERMINAL_OK:
        # A failed/timed-out/terminated RCA run is not a diagnosis to score —
        # scoring it would spend real RAGAS/verdict-judge LLM calls on an
        # empty or partial answer and produce a misleading number.
        row["bucket"] = f"error:rca_{rca_status or 'unknown'}"
        row["detail"] = str(
            rca.get("status_reason") or f"RCA analysis ended with status: {rca_status}"
        )
        row["seconds"] = round(time.monotonic() - t0, 1)
        return row

    row["diagnosis"] = score_diagnosis(scenario, rca)
    metrics = diagnosis_stage_metrics(
        rca, client.account_id, client.tenant_id, client.user_id
    )
    if metrics:
        row["diagnosis_metrics"] = metrics

    fixture_repo = scenario.get("fixture_repo")
    if not fixture_repo:
        row["bucket"] = "diagnosis_only"
    elif diagnosis_only:
        row["bucket"] = "diagnosis_only_run"
    else:
        status_reason = (rca.get("status_reason") or "").strip()
        if status_reason in SETUP_SKIP_REASONS:
            row["bucket"] = SETUP_SKIP_REASONS[status_reason]
        elif status_reason in MODEL_MISS_SKIP_REASONS:
            row["bucket"] = MODEL_MISS_SKIP_REASONS[status_reason]
        else:
            fix_result = verify_fix(fixture_repo, rca.get("git_diff", ""))
            row["bucket"] = fix_result["bucket"]
            row["fix_detail"] = fix_result["detail"]

    row["seconds"] = round(time.monotonic() - t0, 1)
    return row


def run_explore_scenario(code_analyzer: CodeAnalyzerClient, scenario: dict) -> dict:
    row = {"name": scenario["name"], "tags": scenario.get("tags", [])}
    t0 = time.monotonic()

    explore = scenario["explore"]
    response = code_analyzer.query(
        {
            "query": explore["query"],
            "git_repo": explore["git_repo"],
            "git_commit": explore["git_commit"],
            "mode": "explore",
        }
    )
    row["seconds"] = round(time.monotonic() - t0, 1)

    if response.get("_status"):
        row["bucket"] = f"error:{response['_status'].lower()}"
        return row

    scored = score_explore(scenario, response)
    row["explore"] = scored
    verdict_ok = scored["verdict"] in ("passed", "partially_correct")
    citation_ok = scored["citation_grounded"]
    if verdict_ok and citation_ok:
        row["bucket"] = "resolved"
    elif not verdict_ok and not citation_ok:
        row["bucket"] = "failed"
    else:
        row["bucket"] = "partial"
    return row


def run_pr_followup_scenario(code_analyzer: CodeAnalyzerClient, scenario: dict) -> dict:
    row = {"name": scenario["name"], "tags": scenario.get("tags", [])}
    t0 = time.monotonic()

    pr_followup = scenario["pr_followup"]
    fixture_repo = pr_followup["fixture_repo"]

    # Sweep before, not just clean up after: a crashed prior run's PR gets
    # closed by whichever run happens to go next, so no separate cron is
    # needed just for cleanup (docs/code-analysis-eval-spec.md §5c).
    pr_followup_verifier.sweep_orphaned_prs(fixture_repo["url"])

    diff_file = scenario["_dir"] / pr_followup["incomplete_fix_diff_file"]
    setup = pr_followup_verifier.setup_pr(pr_followup, diff_file, scenario["name"])
    if "error" in setup:
        row["bucket"] = "setup_error"
        row["detail"] = setup["error"]
        row["seconds"] = round(time.monotonic() - t0, 1)
        return row

    # try/finally: an exception from either call below must not skip cleanup
    # and leave the PR open — the orphan sweep only catches that on the
    # *next* run, up to _ORPHAN_MAX_AGE_HOURS later.
    try:
        response = code_analyzer.query(
            {
                "followup": True,
                "pr_url": setup["pr_url"],
                "pr_branch": setup["pr_branch"],
                "git_repo": fixture_repo["url"],
            }
        )
        row["followup_seconds"] = round(time.monotonic() - t0, 1)

        if response.get("_status"):
            row["bucket"] = f"error:{response['_status'].lower()}"
            return row

        result = pr_followup_verifier.verify_followup(
            pr_followup,
            setup["pr_url"],
            setup["pr_branch"],
            setup["repo"],
            setup["before_head_sha"],
        )
        row["bucket"] = result["bucket"]
        row["detail"] = result["detail"]
        return row
    finally:
        pr_followup_verifier.cleanup_pr(setup["pr_url"], setup["repo"])
        row["seconds"] = round(time.monotonic() - t0, 1)


def run_one(
    client: EventCodeFixClient,
    code_analyzer: CodeAnalyzerClient,
    scenario: dict,
    diagnosis_only: bool,
) -> dict:
    scenario_type = scenario.get("type", "event")
    if scenario_type == "event":
        return run_event_scenario(client, scenario, diagnosis_only)
    if scenario_type == "explore":
        return run_explore_scenario(code_analyzer, scenario)
    if scenario_type == "pr_followup":
        return run_pr_followup_scenario(code_analyzer, scenario)
    raise ValueError(f"unknown scenario type: {scenario_type!r}")


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument(
        "--scenario-dir", type=Path, default=Path(__file__).parent / "scenarios"
    )
    p.add_argument("--scenario", action="append", help="scenario name; repeatable")
    p.add_argument(
        "--diagnosis-only",
        action="store_true",
        help="skip fix verification even for scenarios with a fixture_repo",
    )
    p.add_argument("--out", type=Path, default=Path("event_code_fix_results.json"))
    args = p.parse_args()

    only = set(args.scenario) if args.scenario else None
    scenarios = load_scenarios(args.scenario_dir, only)
    if not scenarios:
        sys.exit("no scenarios matched")

    rows = []
    done = set()
    if args.out.exists():
        rows = json.loads(args.out.read_text()).get("results", [])
        done = {r["name"] for r in rows}
        if done:
            print(f"resuming: {len(done)} already run", flush=True)

    client = EventCodeFixClient()
    code_analyzer = CodeAnalyzerClient()
    for i, scenario in enumerate(scenarios, 1):
        if scenario["name"] in done:
            continue
        try:
            row = run_one(client, code_analyzer, scenario, args.diagnosis_only)
        except Exception as exc:  # keep the run alive; one bad scenario is not the run
            row = {
                "name": scenario["name"],
                "bucket": "error",
                "detail": f"{type(exc).__name__}: {exc}",
            }
        rows = [r for r in rows if r["name"] != scenario["name"]] + [row]
        print(
            f"[{i}/{len(scenarios)}] {row['name']}: {row.get('bucket', '?')}",
            flush=True,
        )
        args.out.write_text(json.dumps({"results": rows}, indent=1))

    print(f"\nwrote {args.out}")


if __name__ == "__main__":
    main()
