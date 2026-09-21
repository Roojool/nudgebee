"""Dashboard runner for the event_code_fix suite (config.yaml `runner:`).

Drives one scenario through the same pipeline the CLI uses
(event_code_fix/run_event_code_fix.py) and hands the engine the diagnosis text
to score. Anything the engine can't express in RAGAS terms (fix verification
bucket, explore citation check) goes into test_case["_score_note"], which the
engine appends to the result's score reason.

Needs on the benchmark-server (see event_code_fix/README.md):
  API_SERVER_URL, ACTION_API_SERVER_TOKEN, ALERTMANAGER_WEBHOOK_URL  (event scenarios)
  LLM_SERVER_URL, LLM_SERVER_TOKEN                                   (all scenarios)
The webhook token belongs to one account, so run against that account.
"""

from __future__ import annotations

import json
import logging
import os
import sys
from pathlib import Path
from typing import Any

from benchmark_server.utils.llm_client import ErrorCategory, LLMResult

logger = logging.getLogger(__name__)

# llm/benchmark/llm/agents/event_code_fix -> llm/benchmark (/app in the image).
# The suite's modules import each other flat, as they do when run as a CLI.
_SUITE_DIR = Path(__file__).resolve().parents[3] / "event_code_fix"

_EVENT_ENV = (
    "API_SERVER_URL",
    "ACTION_API_SERVER_TOKEN",
    "ALERTMANAGER_WEBHOOK_URL",
    "LLM_SERVER_URL",
    "LLM_SERVER_TOKEN",
)
_EXPLORE_ENV = ("LLM_SERVER_URL", "LLM_SERVER_TOKEN")
_PLACEHOLDER = "REPLACE_WITH_"


def _failed(message: str, category: str = ErrorCategory.AGENT_FAILED) -> tuple:
    return [], LLMResult(
        status="FAILED", error_category=category, error_message=message
    )


def _load_scenario(name: str) -> dict[str, Any]:
    if str(_SUITE_DIR) not in sys.path:
        sys.path.insert(0, str(_SUITE_DIR))
    from scenario_loader import load_scenarios

    scenarios = load_scenarios(_SUITE_DIR / "scenarios", {name})
    if not scenarios:
        raise LookupError(f"no scenario named {name!r} in {_SUITE_DIR / 'scenarios'}")
    return scenarios[0]


def _setup_problem(scenario: dict[str, Any]) -> str | None:
    """Reasons the run can't start — checked up front so a misconfigured
    server costs nothing, instead of burning an RCA on a doomed scenario."""
    if _PLACEHOLDER in json.dumps(scenario, default=str):
        return (
            "fixture repo not provisioned: this scenario still has "
            f"{_PLACEHOLDER}* placeholders (see event_code_fix/fixture_repos/README.md)"
        )
    required = _EVENT_ENV if scenario.get("type", "event") == "event" else _EXPLORE_ENV
    missing = [name for name in required if not os.environ.get(name)]
    if missing:
        return f"benchmark-server is missing env: {', '.join(missing)}"
    return None


def run(
    test_case: dict[str, Any], account_id: str, tenant_id: str, user_id: str
) -> tuple:
    """Engine `runner` contract: (docs, LLMResult). docs is the diagnosis text
    to score, or empty on failure with the reason in LLMResult.error_message."""
    try:
        scenario = _load_scenario(test_case["event_code_fix_scenario"])
        problem = _setup_problem(scenario)
        if problem:
            return _failed(problem, ErrorCategory.CLIENT_ERROR)
        if scenario.get("type", "event") == "event":
            return _run_event(test_case, scenario, account_id, tenant_id, user_id)
        return _run_explore(test_case, scenario, account_id, tenant_id, user_id)
    except Exception as exc:  # one bad scenario must not abort the whole run
        logger.exception("event_code_fix runner failed for %s", test_case.get("__id__"))
        return _failed(f"{type(exc).__name__}: {exc}")


def _run_event(test_case, scenario, account_id, tenant_id, user_id) -> tuple:
    from diagnosis_scorer import diagnosis_text, rca_session_id
    from event_rca_client import EventCodeFixClient
    from run_event_code_fix import run_event_pipeline

    client = EventCodeFixClient(account_id, tenant_id, user_id)
    row, rca = run_event_pipeline(client, scenario, diagnosis_only=False)
    if rca is None:
        return _failed(f"{row['bucket']}: {row.get('detail', '')}")

    answer = diagnosis_text(rca)
    if not answer:
        return _failed("RCA completed but returned no diagnosis text")

    note = f"[event_code_fix] bucket={row['bucket']}"
    if row.get("fix_detail"):
        note += f" — {row['fix_detail']}"
    test_case["_score_note"] = note

    session_id = rca_session_id(rca)
    data = {"data": {"session_id": session_id}} if session_id else None
    return [answer], LLMResult(status="COMPLETED", data=data)


def _run_explore(test_case, scenario, account_id, tenant_id, user_id) -> tuple:
    from code_analyzer_client import CodeAnalyzerClient
    from explore_scorer import citation_grounded
    from run_event_code_fix import query_explore

    client = CodeAnalyzerClient(account_id, tenant_id, user_id)
    row, response = query_explore(client, scenario)
    if response is None:
        return _failed(row["bucket"])

    answer = response.get("answer") or ""
    if not answer:
        return _failed("code_analyzer returned no answer")

    test_case["_score_note"] = (
        f"[event_code_fix] citation_grounded={citation_grounded(scenario, response)}"
    )
    return [answer], LLMResult(status="COMPLETED", data=None)
