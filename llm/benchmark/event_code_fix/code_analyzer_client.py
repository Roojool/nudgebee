"""Direct @code_analyzer entry — explore mode and PR follow-up.

Neither capability is reachable through the event/alert pipeline
event_rca_client.py drives: explore mode is an internal sub-step of
event_analyzer.go's own investigation, and follow-up is triggered in
production by a GitHub webhook or cron backstop, not an event. This mirrors
swebench/run_swebench.py's own pattern instead: a leading "@code_analyzer "
mention bypasses the router (RouterAgent.Execute returns it verbatim before
any LLM routing runs), submitted to /v1/completions/chat and polled via
/v1/completions/chat_get — verified against the same working mechanic
swebench already uses in production, not re-derived from scratch.
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Any

import requests

TERMINAL_OK = {"COMPLETED"}
TERMINAL_BAD = {"FAILED", "TERMINATED", "KILLED"}


def env(name: str, default: str | None = None) -> str:
    v = os.environ.get(name, default)
    if v is None:
        sys.exit(f"{name} must be set")
    return v


class CodeAnalyzerClient:
    def __init__(
        self,
        account_id: str | None = None,
        tenant_id: str | None = None,
        user_id: str | None = None,
    ) -> None:
        self.llm_url = env("LLM_SERVER_URL").rstrip("/")
        self.llm_token = env("LLM_SERVER_TOKEN")
        self.account_id = account_id or env("BENCHMARK_ACCOUNT_ID")
        self.tenant_id = tenant_id or env("BENCHMARK_TENANT_ID")
        self.user_id = user_id or os.environ.get("BENCHMARK_USER_ID", "")
        self.headers = {"X-ACTION-TOKEN": self.llm_token, "x-tenant-id": self.tenant_id}

    def query(
        self,
        payload: dict[str, Any],
        poll_interval: int = 10,
        timeout_seconds: int = 600,
    ) -> dict[str, Any]:
        """Run one @code_analyzer query to completion and return its structured
        JSON response (the same explore-mode/follow-up contract
        submit_analysis.go emits)."""
        body = {
            "query": "@code_analyzer " + json.dumps(payload),
            "account_id": self.account_id,
            "tenant_id": self.tenant_id,
            "user_id": self.user_id,
            "async": True,
        }
        resp = requests.post(
            f"{self.llm_url}/v1/completions/chat",
            json=body,
            headers=self.headers,
            timeout=90,
        )
        resp.raise_for_status()
        started = (resp.json().get("data")) or {}
        cid = started.get("conversation_id")
        if not cid:
            return {"_status": "SUBMIT_FAILED", "_detail": json.dumps(started)[:300]}

        status, data = self._wait(cid, poll_interval, timeout_seconds)
        if status in TERMINAL_BAD or status == "TIMEOUT":
            return {"_status": status, "_conversation_id": cid}
        result = self._code_analyzer_response(data)
        if result is None:
            return {"_status": "NOT_ROUTED_TO_CODE_ANALYZER", "_conversation_id": cid}
        result["_conversation_id"] = cid
        return result

    def converse(
        self,
        text: str,
        conversation_id: str | None = None,
        turn_number: int = 1,
        poll_interval: int = 10,
        timeout_seconds: int = 600,
    ) -> dict[str, Any]:
        """Send one natural-language turn, into an existing conversation when
        conversation_id is given, and return the whole finished conversation.
        turn_number is this turn's 1-based position; it stops the poll from
        mistaking the previous turn's COMPLETED status for this turn's.

        Unlike query(), the text is not prefixed with @code_analyzer, so it goes
        through the normal router exactly as a message typed into Ask Nudgebee
        does. Returns {"_status", "_conversation_id", "conversation"}; conversation
        is the chat_get payload and is only set when the turn completed.
        """
        body: dict[str, Any] = {
            "query": text,
            "account_id": self.account_id,
            "tenant_id": self.tenant_id,
            "user_id": self.user_id,
            "async": True,
        }
        if conversation_id:
            body["conversation_id"] = conversation_id
        resp = requests.post(
            f"{self.llm_url}/v1/completions/chat",
            json=body,
            headers=self.headers,
            timeout=90,
        )
        resp.raise_for_status()
        started = (resp.json().get("data")) or {}
        cid = started.get("conversation_id") or conversation_id
        if not cid:
            return {"_status": "SUBMIT_FAILED", "_detail": json.dumps(started)[:300]}

        status, data = self._wait(cid, poll_interval, timeout_seconds, turn_number)
        result: dict[str, Any] = {"_status": status, "_conversation_id": cid}
        if status in TERMINAL_OK:
            result["conversation"] = data
        return result

    def _wait(
        self, cid: str, poll_interval: int, timeout_seconds: int, min_turns: int = 0
    ) -> tuple[str, dict[str, Any]]:
        """Poll chat_get until the conversation reaches a terminal status with at
        least min_turns user turns recorded. Returns (status, conversation
        payload); status is TIMEOUT if it never did."""
        deadline = time.monotonic() + timeout_seconds
        while time.monotonic() < deadline:
            time.sleep(poll_interval)
            resp = requests.post(
                f"{self.llm_url}/v1/completions/chat_get",
                json={"conversation_id": cid, "account_id": self.account_id},
                headers=self.headers,
                timeout=30,
            )
            resp.raise_for_status()
            conv = resp.json()
            data = conv.get("data") or conv
            status = str(data.get("status", "")).upper()
            if status not in TERMINAL_BAD and status not in TERMINAL_OK:
                continue
            turns = [
                m
                for m in data.get("llm_conversation_messages") or []
                if m.get("message_type", "generation") == "generation"
            ]
            if len(turns) >= min_turns:
                return status, data
        return "TIMEOUT", {}

    @staticmethod
    def _code_analyzer_response(data: dict[str, Any]) -> dict[str, Any] | None:
        for msg in data.get("llm_conversation_messages") or []:
            for agent in msg.get("llm_conversation_agents") or []:
                if agent.get("agent_name") != "code_analyzer":
                    continue
                raw = agent.get("response")
                if not raw:
                    continue
                try:
                    return json.loads(raw)
                except (json.JSONDecodeError, TypeError):
                    return {"_status": "UNPARSEABLE", "_raw": str(raw)[:2000]}
        return None
