"""Unit tests for CommonService.resolve_incident_channel_and_thread -- the
slack.join_channel-bound-channel fallback (#37885).

cache is patched with autospec=True throughout: a plain MagicMock lets a call
with the wrong arity/kwargs silently "succeed" in the test while raising
TypeError against the real Cache class (this bit a real deploy once -- see
Cache.update_event_entry's own thread_ts-named first param)."""

from unittest.mock import MagicMock, patch

from notifications_server.services.common import CommonService


def _service():
    return CommonService(engine=MagicMock(), slack_app=MagicMock(), teams_app=MagicMock())


def test_no_cached_incident_returns_all_none():
    service = _service()
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.return_value = None
        result = service.resolve_incident_channel_and_thread("fp-unknown")

    assert result == (None, None, None, None)


def test_unsupported_platform_returns_all_none():
    service = _service()
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.return_value = {
            "platform": "teams",
            "tenant_id": "t1",
            "team_id": "T1",
            "channel_id": "C1",
        }
        result = service.resolve_incident_channel_and_thread("fp-1")

    assert result == (None, None, None, None)


def test_missing_required_fields_returns_all_none():
    service = _service()
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.return_value = {
            "platform": "slack",
            "tenant_id": "t1",
            "team_id": None,
            "channel_id": "C1",
        }
        result = service.resolve_incident_channel_and_thread("fp-1")

    assert result == (None, None, None, None)


def test_existing_thread_ts_is_reused_without_posting():
    service = _service()
    incident = {
        "platform": "slack",
        "tenant_id": "t1",
        "team_id": "T1",
        "channel_id": "C1",
        "account_id": "acc-1",
        "thread_ts": "1700000000.000000",
    }
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.return_value = incident
        with patch.object(service, "send_channel_message") as send_mock:
            result = service.resolve_incident_channel_and_thread("fp-1")

    assert result == ("C1", "1700000000.000000", "T1", "acc-1")
    send_mock.assert_not_called()
    cache_mock.cache_event_entry.assert_not_called()


def test_first_response_posts_anchor_and_persists_new_thread_ts():
    service = _service()
    incident = {
        "platform": "slack",
        "tenant_id": "t1",
        "team_id": "T1",
        "channel_id": "C1",
        "account_id": "acc-1",
        # No thread_ts yet -- first response for this fingerprint.
    }
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.return_value = incident
        with patch.object(service, "send_channel_message") as send_mock:
            send_mock.return_value = {"success": True, "data": {"message_ts": "1700000001.000000"}}
            result = service.resolve_incident_channel_and_thread("fp-1")

    assert result == ("C1", "1700000001.000000", "T1", "acc-1")
    send_mock.assert_called_once()
    args, kwargs = send_mock.call_args
    assert args[:4] == ("slack", "t1", "C1", "fp-1")
    cache_mock.cache_event_entry.assert_called_once_with("fp-1", {**incident, "thread_ts": "1700000001.000000"})


def test_anchor_post_failure_returns_all_none():
    service = _service()
    incident = {"platform": "slack", "tenant_id": "t1", "team_id": "T1", "channel_id": "C1", "account_id": "acc-1"}
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.return_value = incident
        with patch.object(service, "send_channel_message") as send_mock:
            send_mock.return_value = {"error": {"message": "channel_not_found"}}
            result = service.resolve_incident_channel_and_thread("fp-1")

    assert result == (None, None, None, None)
    cache_mock.cache_event_entry.assert_not_called()


def test_exception_is_caught_and_returns_all_none():
    service = _service()
    with patch("notifications_server.services.common.cache", autospec=True) as cache_mock:
        cache_mock.get_event_entry.side_effect = RuntimeError("redis is down")
        result = service.resolve_incident_channel_and_thread("fp-1")

    assert result == (None, None, None, None)
