"""Repo-continuity scoring for the multi_turn_repo scenario type.

A multi-turn Ask Nudgebee conversation ("find the bug" -> "fix it" -> "now
raise a pr for this") must keep the code agent on the repository the first turn
settled on. Nothing here calls an LLM: every check reads what the conversation
persisted, so a result is reproducible and cheap to re-score.

Two signals per turn, both written by llm-server:
  - the code_analyzer response's source_details, which records the repo the
    agent actually ran against; and
  - the message's stored config (message_config.git_repo), which is where the
    next turn inherits the repo from.
"""

from __future__ import annotations

import json
from typing import Any

BUCKET_RESOLVED = "resolved"
BUCKET_REPO_DRIFT = "repo_drift"
BUCKET_NOT_REMEMBERED = "repo_not_remembered"
BUCKET_NO_CODE_TURN = "no_code_agent_turn"

_REPO_KEY = "workloads.nudgebee.com/git.repo"


def repo_slug(url: str) -> str:
    """owner/name, lower-cased, so a trailing .git or a different URL spelling of
    the same repo compares equal."""
    slug = url.strip().rstrip("/").removesuffix(".git")
    for prefix in ("https://", "http://"):
        slug = slug.removeprefix(prefix)
    # Drop the host: github.com/acme/repo -> acme/repo.
    parts = slug.split("/")
    return "/".join(parts[-2:]).lower() if len(parts) >= 2 else slug.lower()


def pr_repo_slug(pr_url: str) -> str:
    """owner/name of the repository a PR URL belongs to, or "" if it is not one."""
    parts = pr_url.split("/")
    if "pull" not in parts:
        return ""
    return "/".join(parts[parts.index("pull") - 2 : parts.index("pull")])


def _json_or_none(raw: Any) -> dict[str, Any] | None:
    if not raw or not isinstance(raw, str):
        return None
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return None
    return parsed if isinstance(parsed, dict) else None


def _turn_messages(conversation: dict[str, Any]) -> list[dict[str, Any]]:
    """One message per user turn, oldest first. The conversation also holds route,
    followup and response rows that are not turns of their own."""
    messages = [
        m
        for m in conversation.get("llm_conversation_messages") or []
        if m.get("message_type", "generation") == "generation"
    ]
    return sorted(messages, key=lambda m: m.get("created_at") or "")


def turn_repo_facts(conversation: dict[str, Any], turn_index: int) -> dict[str, Any]:
    """What the conversation recorded for the turn_index-th user message.

    Returns {"repo_used": str | None, "repo_remembered": str | None,
    "pr_url": str | None}. repo_used is None when no code_analyzer call ran in
    that turn (the turn was answered without the code agent).
    """
    messages = _turn_messages(conversation)
    if turn_index >= len(messages):
        return {"repo_used": None, "repo_remembered": None, "pr_url": None}
    message = messages[turn_index]

    repo_used = None
    pr_url = None
    for agent in message.get("llm_conversation_agents") or []:
        if agent.get("agent_name") != "code_analyzer":
            continue
        response = _json_or_none(agent.get("response"))
        if not response:
            continue
        details = response.get("source_details") or {}
        repo_used = details.get(_REPO_KEY) or repo_used
        pr_url = response.get("pr_url") or pr_url

    config = _json_or_none(message.get("message_config")) or {}
    return {
        "repo_used": repo_used,
        "repo_remembered": config.get("git_repo") or None,
        "pr_url": pr_url,
    }


def score_repo_continuity(
    conversation: dict[str, Any],
    turn_count: int,
    repo: str,
    distractor_repo: str | None = None,
) -> dict[str, Any]:
    """Score one finished conversation.

    resolved            every code_analyzer turn ran on `repo`, and each of those
                        turns stored it for the next
    repo_drift          some turn ran on a different repository
    repo_not_remembered the turns ran on `repo` but a later turn had nothing
                        stored to inherit it from
    no_code_agent_turn  the code agent never ran, so there is nothing to score

    Returns {"bucket", "detail", "turns"}; turns lists the per-turn facts.
    """
    expected = repo_slug(repo)
    turns = [turn_repo_facts(conversation, i) for i in range(turn_count)]
    code_turns = [(i, t) for i, t in enumerate(turns) if t["repo_used"]]

    if not code_turns:
        return {
            "bucket": BUCKET_NO_CODE_TURN,
            "detail": "no turn ran the code agent",
            "turns": turns,
        }

    for i, turn in code_turns:
        used = repo_slug(turn["repo_used"])
        if used != expected:
            named = (
                " (the distractor)"
                if distractor_repo and used == repo_slug(distractor_repo)
                else ""
            )
            return {
                "bucket": BUCKET_REPO_DRIFT,
                "detail": f"turn {i + 1} ran on {used}{named}, expected {expected}",
                "turns": turns,
            }

    # Every turn but the last has a successor that must inherit the repo.
    last_code_turn = code_turns[-1][0]
    for i, turn in code_turns:
        if i == last_code_turn:
            continue
        remembered = turn["repo_remembered"]
        if not remembered or repo_slug(remembered) != expected:
            return {
                "bucket": BUCKET_NOT_REMEMBERED,
                "detail": f"turn {i + 1} did not store {expected} for the next turn (stored: {remembered!r})",
                "turns": turns,
            }

    return {
        "bucket": BUCKET_RESOLVED,
        "detail": f"{len(code_turns)} code turn(s) stayed on {expected}",
        "turns": turns,
    }
