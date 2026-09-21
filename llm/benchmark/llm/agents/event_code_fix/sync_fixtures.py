"""Generate fixtures/<scenario dir>/test_case.yaml from event_code_fix/scenarios.

scenario.yaml stays the single source of truth; the fixtures only exist
because the dashboard engine discovers tests by globbing fixtures/*/test_case.yaml.
test_dashboard_fixtures.py fails if they drift.

    python -m llm.agents.event_code_fix.sync_fixtures   # from llm/benchmark
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import yaml

AGENT_DIR = Path(__file__).resolve().parent
FIXTURES_DIR = AGENT_DIR / "fixtures"
# llm/benchmark/llm/agents/event_code_fix -> llm/benchmark (/app in the image)
SCENARIOS_DIR = AGENT_DIR.parents[2] / "event_code_fix" / "scenarios"

# pr_followup opens/closes a real PR every run; CLI-only (see config.yaml).
DASHBOARD_TYPES = {"event": "rca", "explore": "question-answer"}


def build_fixture(scenario: dict[str, Any]) -> dict[str, Any]:
    scenario_type = scenario.get("type", "event")
    prompt = (
        scenario["alert"]["summary"]
        if scenario_type == "event"
        else scenario["explore"]["query"]
    )
    return {
        "user_prompt": prompt,
        "expected_output": scenario["expected_diagnosis"],
        "tags": ["event_code_fix", scenario_type, *scenario.get("tags", [])],
        "type": DASHBOARD_TYPES[scenario_type],
        "event_code_fix_scenario": scenario["name"],
    }


def dashboard_scenarios() -> dict[str, dict[str, Any]]:
    """Scenario dir name -> scenario, for every scenario the dashboard runs."""
    scenarios = {}
    for path in sorted(SCENARIOS_DIR.glob("*/scenario.yaml")):
        data = yaml.safe_load(path.read_text())
        if data.get("type", "event") in DASHBOARD_TYPES:
            data.setdefault("name", path.parent.name)
            scenarios[path.parent.name] = data
    return scenarios


def main() -> None:
    for dir_name, scenario in dashboard_scenarios().items():
        out = FIXTURES_DIR / dir_name / "test_case.yaml"
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(
            yaml.safe_dump(build_fixture(scenario), sort_keys=False, width=100)
        )
        print(out.relative_to(AGENT_DIR))


if __name__ == "__main__":
    main()
