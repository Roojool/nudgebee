"""Buttons on Microsoft Teams cards: signed data, the stored card a click
updates, and Suppress."""

import asyncio
import json
import time
import uuid
from types import SimpleNamespace
from unittest import mock

import pytest

from notifications_server.configs import settings
from notifications_server.message_templates.ms_teams.finding import get_ms_teams_finding_message_template
from notifications_server.services import actions as actions_module
from notifications_server.services import actions_common
from notifications_server.services import events as events_module
from notifications_server.services.ms_teams import card_actions as ca

NB_TENANT = "aaaaaaaa-1111-4111-8111-111111111111"
OTHER_TENANT = "bbbbbbbb-2222-4222-8222-222222222222"
ACCOUNT = "cccccccc-3333-4333-8333-333333333333"
SERVICE_URL = "https://smba.example.com/teams/"
FINDING = {
    "id": "event-1",
    "title": "Pod restarting",
    "created_at": "2026-10-07T10:00:00",
    "fingerprint": "fp-1",
    "aggregation_key": "KubePodCrashLooping",
    "cloud_account_id": ACCOUNT,
}


@pytest.fixture(autouse=True)
def signing_key(monkeypatch):
    monkeypatch.setattr(settings.ms_teams, "action_signing_key", "test-signing-key")


# ------------------------------- signed data -------------------------------


def test_signed_data_round_trips_for_its_action_and_tenant():
    raw = ca.sign(ca.SUPPRESS, {"tenant_id": NB_TENANT, "scope": "fingerprint"})
    assert ca.verify(raw, ca.SUPPRESS, NB_TENANT)["scope"] == "fingerprint"
    assert json.loads(raw)["kid"] == ca.KEY_ID


def test_signed_data_is_refused_for_another_tenant_action_or_key():
    raw = ca.sign(ca.SUPPRESS, {"tenant_id": NB_TENANT})
    assert ca.verify(raw, ca.SUPPRESS, OTHER_TENANT) is None
    assert ca.verify(raw, "approve", NB_TENANT) is None
    assert ca.verify(json.dumps({**json.loads(raw), "kid": "0"}), ca.SUPPRESS, NB_TENANT) is None
    settings.ms_teams.action_signing_key = "rotated"
    assert ca.verify(raw, ca.SUPPRESS, NB_TENANT) is None


def test_tampered_or_expired_data_is_refused():
    raw = json.loads(ca.sign(ca.SUPPRESS, {"tenant_id": NB_TENANT, "duration_hours": 1}))
    raw["body"]["action_params"]["duration_hours"] = 168
    assert ca.verify(json.dumps(raw), ca.SUPPRESS, NB_TENANT) is None
    with mock.patch.object(ca.time, "time", return_value=time.time() - ca.MAX_AGE_SECONDS - 60):
        old = ca.sign(ca.SUPPRESS, {"tenant_id": NB_TENANT})
    assert ca.verify(old, ca.SUPPRESS, NB_TENANT) is None
    assert ca.verify("not json", ca.SUPPRESS, NB_TENANT) is None
    assert ca.verify(json.dumps(["not", "a", "dict"]), ca.SUPPRESS, NB_TENANT) is None
    for signature in (12345, None, {"v0": "x"}):
        forged = json.loads(ca.sign(ca.SUPPRESS, {"tenant_id": NB_TENANT}))
        forged["signature"] = signature
        assert ca.verify(json.dumps(forged), ca.SUPPRESS, NB_TENANT) is None


# ------------------------------- card content -------------------------------


def test_a_bot_finding_card_offers_suppress_with_every_choice_signed():
    card = get_ms_teams_finding_message_template(FINDING, NB_TENANT)
    suppress = card["actions"][-1]
    assert suppress["id"] == ca.SUPPRESS_ACTION_ID and ca.has_actions(card)
    choices = suppress["card"]["body"][0]["choices"]
    assert len(choices) == 7
    params = [ca.verify(c["value"], ca.SUPPRESS, NB_TENANT) for c in choices]
    assert {(p["scope"], p["duration_hours"]) for p in params} == {
        ("fingerprint", 1),
        ("fingerprint", 4),
        ("fingerprint", 24),
        ("fingerprint", 168),
        ("alertname", 4),
        ("alertname", 24),
        ("alertname", 168),
    }
    assert suppress["card"]["actions"][0]["data"] == {ca.ACTION_KEY: ca.SUPPRESS}
    assert len(json.dumps(card)) < 28_000


def test_cards_have_no_buttons_without_a_signing_key_or_for_webhooks():
    assert not ca.has_actions(get_ms_teams_finding_message_template(FINDING)), "webhook cards can't be clicked"
    settings.ms_teams.action_signing_key = ""
    assert not ca.has_actions(get_ms_teams_finding_message_template(FINDING, NB_TENANT))


def test_status_lines_replace_each_other_and_actions_can_be_removed():
    card = get_ms_teams_finding_message_template(FINDING, NB_TENANT)
    card = ca.with_status(ca.with_status(card, "Suppressing…"), "Suppressed")
    assert [b["text"] for b in card["body"] if b.get("id") == ca.STATUS_ID] == ["Suppressed"]
    assert ca.with_status(card, None)["body"][-1].get("id") != ca.STATUS_ID
    assert not ca.has_actions(ca.without_action(card, ca.SUPPRESS_ACTION_ID))


# ------------------------------- stored card -------------------------------


@pytest.fixture
def store(monkeypatch):
    from sqlalchemy import create_engine
    from sqlalchemy.orm import Session
    from sqlalchemy.pool import StaticPool

    from notifications_server.models.models import Base, SentNotifications

    engine = create_engine("sqlite:///:memory:", connect_args={"check_same_thread": False}, poolclass=StaticPool)
    Base.metadata.create_all(engine, tables=[SentNotifications.__table__])
    monkeypatch.setattr(ca, "sync_engine", engine)
    card = get_ms_teams_finding_message_template(FINDING, NB_TENANT)
    row = ca.card_row(uuid.UUID(NB_TENANT), ACCOUNT, "fp-1", "19:ops", "1700", card, "finding")
    row.id = uuid.uuid4()
    row.account_id = uuid.UUID(ACCOUNT)
    with Session(engine) as session:
        session.add(row)
        session.commit()

    def stored():
        with Session(engine) as session:
            return json.loads(session.query(SentNotifications).one().teams_metadata)

    return SimpleNamespace(engine=engine, stored=stored)


def test_a_card_row_holds_the_card_and_falls_back_to_the_message_id():
    row = ca.card_row(NB_TENANT, None, None, "19:ops", "1700", {"type": "AdaptiveCard"}, "finding")
    assert row.fingerprint == "ms_teams:1700"
    assert row.teams_channel_id == "19:ops" and row.teams_message_id == "1700"
    assert json.loads(row.teams_metadata) == {"kind": "finding", "card": {"type": "AdaptiveCard"}, "actions": {}}
    assert ca.card_row(NB_TENANT, None, None, "19:ops", "", {}, "finding") is None, "a click finds its card by id"
    threaded = ca.card_row(NB_TENANT, None, None, "19:ops;messageid=1", "1700", {}, "finding")
    assert threaded.teams_channel_id == "19:ops", "stored the way a click names its channel"


CARD = (uuid.UUID(NB_TENANT), "19:ops", "1700")


def test_an_action_runs_once(store):
    card = ca.begin(CARD, ca.SUPPRESS, lambda c: ca.with_status(c, "Suppressing…"))
    assert card["body"][-1]["text"] == "Suppressing…"
    assert ca.begin(CARD, ca.SUPPRESS, lambda c: c) is ca.BUSY, "a second click while it runs"
    ca.finish(CARD, ca.SUPPRESS, ca.DONE, lambda c: ca.with_status(c, "Suppressed"))
    assert store.stored()["actions"][ca.SUPPRESS]["state"] == ca.DONE
    assert ca.begin(CARD, ca.SUPPRESS, lambda c: c) is ca.REFUSED, "a click after it finished"


def test_an_abandoned_action_can_run_again(store):
    ca.begin(CARD, ca.SUPPRESS, lambda c: ca.with_status(c, "Suppressing…"))
    later = time.time() + ca.IN_PROGRESS_STALE_SECONDS + 1
    with mock.patch.object(ca.time, "time", return_value=later):
        assert ca.begin(CARD, ca.SUPPRESS, lambda c: c) not in (None, ca.REFUSED)


def test_a_failed_action_can_be_tried_again(store):
    ca.begin(CARD, ca.SUPPRESS, lambda c: ca.with_status(c, "Suppressing…"))
    card = ca.finish(CARD, ca.SUPPRESS, None, lambda c: ca.with_status(c, None))
    assert all(b.get("id") != ca.STATUS_ID for b in card["body"])
    assert ca.begin(CARD, ca.SUPPRESS, lambda c: c) not in (None, ca.REFUSED)


def test_another_tenant_channel_or_card_has_no_stored_card(store):
    assert ca.begin((uuid.UUID(OTHER_TENANT), "19:ops", "1700"), ca.SUPPRESS, lambda c: c) is None
    assert ca.begin((uuid.UUID(NB_TENANT), "19:other", "1700"), ca.SUPPRESS, lambda c: c) is None
    assert ca.begin((uuid.UUID(NB_TENANT), "19:ops", "9999"), ca.SUPPRESS, lambda c: c) is None


def test_a_finding_card_with_buttons_is_returned_with_its_row():
    from notifications_server.services.message import MessageService

    service = MessageService.__new__(MessageService)
    ip = SimpleNamespace(tenant_id=NB_TENANT)
    sent = {"status": "success", "team_id": "group-1", "channel_id": "19:ops", "message_ts": "1700"}
    with mock.patch.object(MessageService, "_send_teams_template_as_bot", mock.AsyncMock(return_value=sent)):
        response, row = asyncio.run(service._send_finding_to_teams(None, NB_TENANT, ip, FINDING, "fp-1"))
    assert response is sent
    assert (row.teams_message_id, row.fingerprint, row.teams_channel_id) == ("1700", "fp-1", "19:ops")
    assert ca.has_actions(json.loads(row.teams_metadata)["card"])

    settings.ms_teams.action_signing_key = ""
    with mock.patch.object(MessageService, "_send_teams_template_as_bot", mock.AsyncMock(return_value=sent)):
        assert asyncio.run(service._send_finding_to_teams(None, NB_TENANT, ip, FINDING, "fp-1"))[1] is None


# ------------------------------- Suppress -------------------------------


def _click(store_card=True):
    card = get_ms_teams_finding_message_template(FINDING, NB_TENANT)
    choice = card["actions"][-1]["card"]["body"][0]["choices"][1]["value"]  # this alert, 4 hours
    return SimpleNamespace(
        value={ca.ACTION_KEY: ca.SUPPRESS, ca.SUPPRESS_CHOICE: choice},
        reply_to_id="1700",
        service_url=SERVICE_URL,
        conversation=SimpleNamespace(id="19:ops;messageid=1700"),
        from_property=SimpleNamespace(name="Dana"),
    )


def _run(activity, *, tenant=NB_TENANT, user=("user-1", [NB_TENANT]), allowed=True, created=True, claimed=True):
    common = SimpleNamespace(teams_reply=mock.AsyncMock())
    sender = mock.Mock()
    with (
        mock.patch.object(ca, "_tenant_for", return_value=uuid.UUID(tenant) if tenant else None),
        mock.patch.object(actions_module, "validate_and_get_user_tenants", return_value=user),
        mock.patch.object(actions_common, "can_manage_triage_rules", return_value=allowed) as permission,
        mock.patch.object(actions_common, "create_suppress_rule", return_value=created) as create,
        mock.patch.object(ca.Cache, "claim_card_action", return_value=claimed) as claim,
        mock.patch.object(ca.Cache, "release_card_action") as release,
        mock.patch.object(ca, "get_sender", return_value=sender),
    ):
        asyncio.run(ca.handle(common, activity, "dana@example.com", "aad-tenant"))
    replies = [c.args[1] for c in common.teams_reply.call_args_list]
    return SimpleNamespace(
        replies=replies, sender=sender, permission=permission, create=create, claim=claim, release=release
    )


def test_an_admin_suppresses_the_alert_and_the_card_says_so(store):
    result = _run(_click())
    rule_input = result.create.call_args.args[2]
    assert result.create.call_args.args[:2] == (NB_TENANT, "user-1")
    assert rule_input["match_fingerprint"] == "fp-1" and rule_input["cloud_account_id"] == ACCOUNT
    assert rule_input["name"] == "Suppressed via Microsoft Teams by dana@example.com"
    assert result.permission.call_args.args == ("user-1", NB_TENANT, ACCOUNT)

    updates = [c.args for c in result.sender.update.call_args_list]
    assert [u[:3] for u in updates] == [(SERVICE_URL, "19:ops;messageid=1700", "1700")] * 2
    first, last = (u[3].attachments[0].content for u in updates)
    assert first["body"][-1]["text"] == "Suppressing…"
    assert last["body"][-1]["text"].startswith("Suppressed (this alert · 4h) by Dana · until ")
    assert not ca.has_actions(last), "the menu is gone once it's done"
    assert "Open Triage Rules" in result.replies[0]
    assert store.stored()["actions"][ca.SUPPRESS]["state"] == ca.DONE
    assert result.claim.call_args.args[0] == f"{NB_TENANT}:1700:{ca.SUPPRESS}"


def test_a_second_click_after_suppressing_says_so_and_does_nothing(store):
    _run(_click())
    again = _run(_click())
    again.create.assert_not_called()
    assert again.replies == [ca.ALREADY_SUPPRESSED]


def test_a_click_while_the_first_still_runs_says_so(store):
    ca.begin(CARD, ca.SUPPRESS, lambda c: ca.with_status(c, "Suppressing…"))
    again = _run(_click())
    again.create.assert_not_called()
    assert again.replies == [ca.STILL_SUPPRESSING]


def test_a_click_that_doesnt_name_its_card_is_refused_and_touches_nothing(store):
    activity = _click()
    activity.reply_to_id = None
    result = _run(activity)
    result.create.assert_not_called()
    result.claim.assert_not_called()
    assert result.replies == [actions_common.UNABLE_TO_PROCESS_REQUEST]
    assert store.stored()["actions"] == {}


def test_someone_who_isnt_an_admin_is_told_why(store):
    result = _run(_click(), allowed=False)
    result.create.assert_not_called()
    assert result.replies == [actions_common.SUPPRESS_NOT_ALLOWED_MESSAGE]
    result.sender.update.assert_not_called()


def test_someone_outside_the_workspace_is_refused(store):
    result = _run(_click(), user=("user-9", [OTHER_TENANT]))
    result.permission.assert_not_called()
    assert result.replies == [actions_common.USER_NOT_FOUND_MESSAGE]


def test_a_click_from_an_unconnected_or_other_microsoft_tenant_is_refused(store):
    for tenant in (None, OTHER_TENANT):
        result = _run(_click(), tenant=tenant)
        result.create.assert_not_called()
        assert result.replies == [actions_common.UNABLE_TO_PROCESS_REQUEST]


def test_no_choice_asks_for_one(store):
    activity = _click()
    activity.value.pop(ca.SUPPRESS_CHOICE)
    assert _run(activity).replies == [ca.PICK_A_DURATION]


def test_a_failed_suppression_clears_the_card_and_can_be_retried(store):
    result = _run(_click(), created=False)
    assert result.replies == [actions_common.UNABLE_TO_PROCESS_REQUEST]
    result.release.assert_called_once_with(f"{NB_TENANT}:1700:{ca.SUPPRESS}")
    last = result.sender.update.call_args_list[-1].args[3].attachments[0].content
    assert all(b.get("id") != ca.STATUS_ID for b in last["body"])
    assert store.stored()["actions"] == {}
    assert _run(_click()).create.called


def test_a_card_without_a_stored_copy_still_suppresses_and_replies(store, monkeypatch):
    from sqlalchemy.orm import Session

    from notifications_server.models.models import SentNotifications

    with Session(store.engine) as session:
        session.query(SentNotifications).delete()
        session.commit()
    result = _run(_click())
    result.create.assert_called_once()
    result.sender.update.assert_not_called()
    assert "Open Triage Rules" in result.replies[0]


def test_a_failed_card_update_still_reports_in_the_thread(store):
    from notifications_server.services.ms_teams.bot_sender import TeamsBotError

    common = SimpleNamespace(teams_reply=mock.AsyncMock())
    sender = mock.Mock()
    sender.update.side_effect = TeamsBotError("Microsoft Teams returned 404", status=404)
    with (
        mock.patch.object(ca, "_tenant_for", return_value=uuid.UUID(NB_TENANT)),
        mock.patch.object(actions_module, "validate_and_get_user_tenants", return_value=("user-1", [NB_TENANT])),
        mock.patch.object(actions_common, "can_manage_triage_rules", return_value=True),
        mock.patch.object(actions_common, "create_suppress_rule", return_value=True),
        mock.patch.object(ca.Cache, "claim_card_action", return_value=True),
        mock.patch.object(ca.Cache, "release_card_action"),
        mock.patch.object(ca, "get_sender", return_value=sender),
    ):
        asyncio.run(ca.handle(common, _click(), "dana@example.com", "aad-tenant"))
    assert "Open Triage Rules" in common.teams_reply.call_args.args[1]


# ------------------------------- routing -------------------------------


def test_a_button_click_is_routed_to_the_card_action_not_the_empty_message_reply():
    events = events_module.Events.__new__(events_module.Events)
    events.common_service = SimpleNamespace(get_teams_user_info=mock.AsyncMock(return_value="dana@example.com"))
    activity = SimpleNamespace(
        conversation=SimpleNamespace(id="19:ops;messageid=1700"),
        from_property=SimpleNamespace(aad_object_id="user-1", role="user"),
        text=None,
        id="1701",
        channel_data={"tenant": {"id": "aad-tenant"}},
        value={ca.ACTION_KEY: ca.SUPPRESS},
    )
    with (
        mock.patch.object(ca, "handle", mock.AsyncMock()) as handle,
        mock.patch.object(events_module.Events, "_reply_empty_message", mock.AsyncMock()) as empty,
    ):
        asyncio.run(events.handle_message_event(activity))
    handle.assert_awaited_once_with(events.common_service, activity, "dana@example.com", "aad-tenant")
    empty.assert_not_awaited()


# ------------------------------- review follow-ups -------------------------------


def test_a_card_too_large_for_the_menu_is_still_sent_without_it():
    finding = {**FINDING, "evidences": [{"type": "markdown", "data": "log line " * 120} for _ in range(23)]}
    card = get_ms_teams_finding_message_template(finding, NB_TENANT)
    assert not ca.has_actions(card), "no room for the menu"
    assert card["actions"][0]["title"] == "View", "the alert itself is still built"
    assert ca.has_actions(get_ms_teams_finding_message_template(FINDING, NB_TENANT))


def test_a_click_on_a_card_older_than_its_buttons_says_so(store):
    with mock.patch.object(ca.time, "time", return_value=time.time() - ca.MAX_AGE_SECONDS - 3600):
        activity = _click()
    result = _run(activity)
    result.create.assert_not_called()
    assert result.replies == [ca.SUPPRESS_CARD_TOO_OLD]


def test_a_click_while_another_holds_the_claim_says_its_running(store):
    result = _run(_click(), claimed=False)
    result.create.assert_not_called()
    assert result.replies == [ca.STILL_SUPPRESSING]


def test_the_claim_is_released_however_the_action_ends(store):
    assert _run(_click()).release.call_count == 1, "after success"
    assert _run(_click(), created=False).release.call_count == 1, "after a failed rule"


def test_an_unexpected_error_replies_and_releases_the_claim(store):
    with mock.patch.object(ca, "begin", side_effect=RuntimeError("db down")):
        result = _run(_click())
    assert result.replies == [actions_common.UNABLE_TO_PROCESS_REQUEST]
    result.release.assert_called_once()
