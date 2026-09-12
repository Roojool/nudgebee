"""Regression test: get_cached_or_fallback_entry's auto-created cache entry
(for a thread_ts never seen before) never included tenant_id. For a plain
chat thread that's harmless -- nothing reads it -- but for a bound
event-investigation follow-up it made execute_event's
`if not (account_id and tenant_id):` guard treat every reply as a brand-new
conversation, wiping the pending follow-up instead of answering it
(nudgebee-enterprise#37885). Fixed by seeding tenant_id from payload.tenant_id
at creation time, same as every other field already there."""

import asyncio
from unittest.mock import MagicMock

from notifications_server.routers import llm_callbacks
from notifications_server.routers.llm_callbacks import LLMResponse, get_cached_or_fallback_entry


def test_fallback_entry_includes_tenant_id(monkeypatch):
    monkeypatch.setattr(llm_callbacks.event_cache, "get_event_entry", lambda thread_ts: None)
    monkeypatch.setattr(llm_callbacks, "_lookup_team_id", lambda tenant_id: "T05JWTTH3NH")
    captured = {}
    monkeypatch.setattr(
        llm_callbacks.event_cache,
        "cache_event_entry",
        lambda thread_ts, entry: captured.update({"thread_ts": thread_ts, "entry": dict(entry)}),
    )

    payload = LLMResponse(
        conversation_id="C1-1700000000.000000",
        type="final",
        response="done",
        tenant_id="tenant-1",
    )

    cached_entry, channel_id, thread_ts, team_id, account_id = asyncio.run(
        get_cached_or_fallback_entry(MagicMock(), payload)
    )

    assert cached_entry["tenant_id"] == "tenant-1"
    assert captured["entry"]["tenant_id"] == "tenant-1"
