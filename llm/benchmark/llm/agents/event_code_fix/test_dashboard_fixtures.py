"""Dashboard wiring for the event_code_fix suite: fixtures track the scenarios,
the runner maps pipeline outcomes onto the engine's (docs, LLMResult) contract,
and the engine actually delegates to it."""

import pytest
import yaml

from benchmark_server.utils.llm_client import ErrorCategory
from llm.agents.common import benchmark
from llm.agents.event_code_fix import runner
from llm.agents.event_code_fix.sync_fixtures import (
    AGENT_DIR,
    FIXTURES_DIR,
    build_fixture,
    dashboard_scenarios,
)

EVENT_ENV = {
    "API_SERVER_URL": "http://api",
    "ACTION_API_SERVER_TOKEN": "t",
    "ALERTMANAGER_WEBHOOK_URL": "http://api/hook?token=x",
    "LLM_SERVER_URL": "http://llm",
    "LLM_SERVER_TOKEN": "t",
}


@pytest.fixture
def event_env(monkeypatch):
    for k, v in EVENT_ENV.items():
        monkeypatch.setenv(k, v)


def _test_case(scenario_name):
    return {"__id__": scenario_name, "event_code_fix_scenario": scenario_name}


def test_fixtures_match_scenarios():
    """Regenerate with: python -m llm.agents.event_code_fix.sync_fixtures"""
    expected = {
        dir_name: build_fixture(scenario)
        for dir_name, scenario in dashboard_scenarios().items()
    }
    actual = {
        p.parent.name: yaml.safe_load(p.read_text())
        for p in FIXTURES_DIR.glob("*/test_case.yaml")
    }
    assert actual == expected


def test_pr_followup_is_not_a_dashboard_fixture():
    assert not any(
        "pr_followup" in p.parent.name for p in FIXTURES_DIR.glob("*/*.yaml")
    )


def test_config_points_at_the_runner():
    config = yaml.safe_load((AGENT_DIR / "config.yaml").read_text())
    assert benchmark._import_callable(config["runner"]) is runner.run
    assert config["max_retries"] == 0


def test_unprovisioned_fixture_repo_fails_fast_without_network(event_env):
    docs, result = runner.run(_test_case("memory_leak_oom"), "acc", "ten", "usr")
    assert docs == []
    assert result.error_category == ErrorCategory.CLIENT_ERROR
    assert "fixture repo not provisioned" in result.error_message


def test_missing_server_env_is_reported_by_name(monkeypatch):
    for k in EVENT_ENV:
        monkeypatch.delenv(k, raising=False)
    docs, result = runner.run(
        _test_case("undersized_memory_limit_oom"), "acc", "ten", "usr"
    )
    assert docs == []
    assert "ALERTMANAGER_WEBHOOK_URL" in result.error_message


def test_event_scenario_returns_diagnosis_and_fix_note(event_env, monkeypatch):
    runner._load_scenario("undersized_memory_limit_oom")  # puts the suite on sys.path
    import run_event_code_fix

    rca = {
        "status": "completed",
        "summary": "limit too low",
        "investigation": "spike at batch 88213",
        "event_fingerprint": "fp1",
    }
    monkeypatch.setattr(
        run_event_code_fix,
        "run_event_pipeline",
        lambda client, scenario, diagnosis_only: (
            {"bucket": "diagnosis_only"},
            rca,
        ),
    )
    tc = _test_case("undersized_memory_limit_oom")
    docs, result = runner.run(tc, "acc", "ten", "usr")

    assert docs == ["limit too low\n\nspike at batch 88213"]
    assert result.status == "COMPLETED"
    assert result.data == {"data": {"session_id": "event-rca-fp1"}}
    assert tc["_score_note"] == "[event_code_fix] bucket=diagnosis_only"


def test_pipeline_failure_is_a_non_retryable_failed_result(event_env, monkeypatch):
    runner._load_scenario("undersized_memory_limit_oom")
    import run_event_code_fix

    monkeypatch.setattr(
        run_event_code_fix,
        "run_event_pipeline",
        lambda *a, **k: ({"bucket": "ingestion_timeout", "detail": "no event"}, None),
    )
    docs, result = runner.run(
        _test_case("undersized_memory_limit_oom"), "acc", "ten", "usr"
    )
    assert docs == []
    assert result.error_message == "ingestion_timeout: no event"
    assert result.error_category not in ErrorCategory.RETRYABLE


def test_exception_in_pipeline_does_not_escape_the_runner(event_env, monkeypatch):
    runner._load_scenario("undersized_memory_limit_oom")
    import run_event_code_fix

    def boom(*a, **k):
        raise ConnectionError("webhook down")

    monkeypatch.setattr(run_event_code_fix, "run_event_pipeline", boom)
    docs, result = runner.run(
        _test_case("undersized_memory_limit_oom"), "acc", "ten", "usr"
    )
    assert docs == []
    assert "ConnectionError: webhook down" in result.error_message


def test_explore_scenario_returns_answer_and_citation_note(event_env, monkeypatch):
    scenario = {
        "name": "x",
        "type": "explore",
        "explore": {"query": "q", "git_repo": "r", "git_commit": "abc"},
        "expected_citation": {"file_contains": "cache.py"},
    }
    runner._load_scenario("undersized_memory_limit_oom")  # puts the suite on sys.path
    import run_event_code_fix

    monkeypatch.setattr(runner, "_load_scenario", lambda name: scenario)
    monkeypatch.setattr(
        run_event_code_fix,
        "query_explore",
        lambda client, sc: (
            {},
            {
                "answer": "put() never evicts",
                "citations": [{"file_path": "a/cache.py"}],
            },
        ),
    )
    tc = _test_case("x")
    docs, result = runner.run(tc, "acc", "ten", "usr")

    assert docs == ["put() never evicts"]
    assert tc["_score_note"] == "[event_code_fix] citation_grounded=True"


def test_engine_delegates_to_the_runner_instead_of_calling_the_llm(monkeypatch):
    def no_llm(*a, **k):
        raise AssertionError("call_llm must not run when a runner is configured")

    monkeypatch.setattr(benchmark, "call_llm", no_llm)
    seen = {}

    def fake_runner(test_case, account_id, tenant_id, user_id):
        seen["args"] = (test_case, account_id, tenant_id, user_id)
        return ["answer"], "llm-result"

    docs, llm_result, elapsed = benchmark._execute_query(
        "prompt", "event_code_fix", "acc", "ten", "usr", None,
        benchmark._default_answer_extractor, [], None, False,
        fake_runner, {"id": 1},
    )  # fmt: skip

    assert (docs, llm_result) == (["answer"], "llm-result")
    assert seen["args"] == ({"id": 1}, "acc", "ten", "usr")
    assert elapsed >= 0
