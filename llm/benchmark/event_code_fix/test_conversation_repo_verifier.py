import json

import pytest

import code_analyzer_client
import conversation_repo_verifier as v

REPO_A = "https://github.com/acme/service-a"
REPO_B = "https://github.com/acme/service-b"
REPO_KEY = "workloads.nudgebee.com/git.repo"


def turn(
    used=None, remembered=None, pr_url=None, message_type="generation", created="1"
):
    """One conversation message, as chat_get returns it."""
    message = {"message_type": message_type, "created_at": created}
    if remembered:
        message["message_config"] = json.dumps({"git_repo": remembered})
    if used:
        response = {"source_details": {REPO_KEY: used}}
        if pr_url:
            response["pr_url"] = pr_url
        message["llm_conversation_agents"] = [
            {"agent_name": "code_analyzer", "response": json.dumps(response)}
        ]
    return message


def conversation(*messages):
    return {"llm_conversation_messages": list(messages)}


def test_every_turn_on_the_repo_and_remembered_is_resolved():
    conv = conversation(
        turn(REPO_A, REPO_A, created="1"),
        turn(REPO_A, REPO_A, created="2"),
        turn(REPO_A, REPO_A, created="3"),
    )
    result = v.score_repo_continuity(conv, 3, REPO_A, REPO_B)
    assert result["bucket"] == v.BUCKET_RESOLVED


def test_a_later_turn_on_another_repo_is_drift():
    conv = conversation(
        turn(REPO_A, REPO_A, created="1"),
        turn(REPO_A, REPO_A, created="2"),
        turn(REPO_B, REPO_B, created="3"),
    )
    result = v.score_repo_continuity(conv, 3, REPO_A, REPO_B)
    assert result["bucket"] == v.BUCKET_REPO_DRIFT
    assert "turn 3" in result["detail"] and "distractor" in result["detail"]


def test_a_turn_that_stored_nothing_for_the_next_is_not_remembered():
    conv = conversation(
        turn(REPO_A, None, created="1"),
        turn(REPO_A, REPO_A, created="2"),
    )
    result = v.score_repo_continuity(conv, 2, REPO_A)
    assert result["bucket"] == v.BUCKET_NOT_REMEMBERED
    assert "turn 1" in result["detail"]


def test_the_last_code_turn_needs_no_stored_repo():
    conv = conversation(
        turn(REPO_A, REPO_A, created="1"), turn(REPO_A, None, created="2")
    )
    assert v.score_repo_continuity(conv, 2, REPO_A)["bucket"] == v.BUCKET_RESOLVED


def test_no_code_agent_turn_has_nothing_to_score():
    conv = conversation(turn(created="1"), turn(created="2"))
    assert v.score_repo_continuity(conv, 2, REPO_A)["bucket"] == v.BUCKET_NO_CODE_TURN


def test_route_and_followup_rows_are_not_turns():
    conv = conversation(
        turn(REPO_A, REPO_A, created="1"),
        turn(REPO_B, REPO_B, message_type="route", created="2"),
        turn(REPO_A, REPO_A, created="3"),
    )
    assert v.score_repo_continuity(conv, 2, REPO_A)["bucket"] == v.BUCKET_RESOLVED


def test_turns_are_ordered_by_creation_not_by_position():
    conv = conversation(
        turn(REPO_B, REPO_B, created="2"), turn(REPO_A, REPO_A, created="1")
    )
    assert v.turn_repo_facts(conv, 0)["repo_used"] == REPO_A


@pytest.mark.parametrize(
    "spelling",
    [
        REPO_A,
        REPO_A + ".git",
        REPO_A + "/",
        "https://github.com/Acme/Service-A",
        "github.com/acme/service-a",
    ],
)
def test_repo_spellings_compare_equal(spelling):
    assert v.repo_slug(spelling) == "acme/service-a"


def test_pr_repo_slug():
    assert (
        v.pr_repo_slug("https://github.com/acme/service-a/pull/12") == "acme/service-a"
    )
    assert v.pr_repo_slug("not a pr") == ""


class FakeResponse:
    def __init__(self, payload):
        self._payload = payload

    def raise_for_status(self):
        pass

    def json(self):
        return self._payload


@pytest.fixture
def client(monkeypatch):
    for name, value in {
        "LLM_SERVER_URL": "http://llm",
        "LLM_SERVER_TOKEN": "t",
        "BENCHMARK_ACCOUNT_ID": "acc",
        "BENCHMARK_TENANT_ID": "ten",
    }.items():
        monkeypatch.setenv(name, value)
    monkeypatch.setattr(code_analyzer_client.time, "sleep", lambda _s: None)
    return code_analyzer_client.CodeAnalyzerClient()


def test_converse_does_not_mistake_the_previous_turns_status_for_its_own(
    client, monkeypatch
):
    """Right after turn 2 is submitted, chat_get can still report turn 1's
    COMPLETED. The poll must wait until the second turn is recorded."""
    polls = iter(
        [
            {
                "data": {
                    "status": "COMPLETED",
                    "llm_conversation_messages": [{"message_type": "generation"}],
                }
            },
            {
                "data": {
                    "status": "COMPLETED",
                    "llm_conversation_messages": [{"message_type": "generation"}] * 2,
                }
            },
        ]
    )
    posted = []

    def fake_post(url, json=None, **_kwargs):
        posted.append((url, json))
        if url.endswith("/chat"):
            return FakeResponse({"data": {"conversation_id": "c1"}})
        return FakeResponse(next(polls))

    monkeypatch.setattr(code_analyzer_client.requests, "post", fake_post)

    result = client.converse("now raise a pr for this", "c1", turn_number=2)

    assert result["_status"] == "COMPLETED"
    assert len(result["conversation"]["llm_conversation_messages"]) == 2
    submit = posted[0][1]
    assert submit["conversation_id"] == "c1"
    assert submit["query"] == "now raise a pr for this"  # not routed via @code_analyzer


def test_query_still_returns_the_code_analyzer_response(client, monkeypatch):
    response = {"answer": "x"}
    conversation = {
        "status": "COMPLETED",
        "llm_conversation_messages": [
            {
                "llm_conversation_agents": [
                    {"agent_name": "code_analyzer", "response": json.dumps(response)}
                ]
            }
        ],
    }

    def fake_post(url, json=None, **_kwargs):
        if url.endswith("/chat"):
            return FakeResponse({"data": {"conversation_id": "c1"}})
        return FakeResponse({"data": conversation})

    monkeypatch.setattr(code_analyzer_client.requests, "post", fake_post)
    assert client.query({"query": "q"}) == {"answer": "x", "_conversation_id": "c1"}
