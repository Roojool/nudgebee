"""Buttons on Microsoft Teams cards: the signed data they carry, the stored copy
of each card that a click updates, and the actions themselves.

A click is an Action.Submit: Teams sends it as a message activity whose value
holds the button's data, with replyToId naming the card. Teams can't return a
posted card, so the card each button sits on is stored in sent_notifications
when it's sent; a click locks that row, stamps the stored card and writes the
result back to Teams."""

import asyncio
import json
import logging
import time
from datetime import datetime, timedelta, timezone
from typing import Callable, Optional

from sqlalchemy.orm import Session

from notifications_server import sync_engine
from notifications_server.configs.settings import URLRoutes, settings
from notifications_server.models.models import SentNotifications
from notifications_server.services.cache import Cache
from notifications_server.services.messaging_installations import ms_teams_aad_tenant_owner
from notifications_server.services.ms_teams.bot_sender import TeamsBotError, card_activity, get_sender
from notifications_server.utils.action_requests import ActionRequestBody, sign_action_request, verify_action_request
from notifications_server.utils.datetime_utils import utc_now

LOG = logging.getLogger(__name__)

ACTION_KEY = "nb_action"
SUPPRESS = "suppress"
SUPPRESS_CHOICE = "nb_suppress_choice"
SUPPRESS_ACTION_ID = "nb_suppress"
STATUS_ID = "nb_status"

# Identifies the signing key, so a later key can be verified alongside this one.
KEY_ID = "1"
# How long a card's buttons stay valid. api-server's sent_notifications cleanup
# keeps a Teams card's row exactly this long (teamsCardRetentionDays).
MAX_AGE_SECONDS = 7 * 24 * 3600
# Teams refuses a message over about 28 KB; buttons are only added to a card
# that stays under this with them.
CARD_SIZE_LIMIT = 25_000
CLAIM_SECONDS = 120
# An action still marked in progress after this long is treated as abandoned
# (e.g. the pod handling it stopped), so it can be run again.
IN_PROGRESS_STALE_SECONDS = 600
IN_PROGRESS, DONE = "in_progress", "done"
REFUSED = object()  # the action already ran on this card
BUSY = object()  # the action is running on this card now

SUPPRESS_THIS_DURATIONS = (("1 hour", 1), ("4 hours", 4), ("24 hours", 24), ("7 days", 168))
SUPPRESS_ALL_DURATIONS = (("4 hours", 4), ("24 hours", 24), ("7 days", 168))
PICK_A_DURATION = "Pick how long to suppress the alert for, then press Suppress."
ALREADY_SUPPRESSED = "This alert was already suppressed from this card."
STILL_SUPPRESSING = "Still suppressing this alert. It'll show on the card in a moment."
SUPPRESS_CARD_TOO_OLD = (
    "This card is more than 7 days old, so its Suppress menu no longer works."
    " Suppress the alert from Triage Rules in NudgeBee."
)


def buttons_enabled() -> bool:
    return bool(settings.ms_teams.action_signing_key)


# ------------------------------- signed data -------------------------------


def sign(action_name: str, params: dict) -> str:
    body = ActionRequestBody(
        timestamp=int(time.time()), action_name=action_name, action_params=params, origin="ms_teams"
    )
    signature = sign_action_request(body, settings.ms_teams.action_signing_key)
    return json.dumps({"kid": KEY_ID, "body": body.model_dump(exclude_none=True), "signature": signature})


def _checked(raw, action_name: str, tenant_id):
    """(params, age in seconds) of button data we signed for this action and this
    NudgeBee tenant, or None."""
    try:
        payload = json.loads(raw) if isinstance(raw, str) else raw
        if payload.get("kid") != KEY_ID:
            return None
        body = ActionRequestBody(**(payload.get("body") or {}))
        if body.action_name != action_name:
            return None
        if not verify_action_request(body, payload.get("signature") or "", settings.ms_teams.action_signing_key):
            return None
    except Exception:
        return None
    params = body.action_params or {}
    if not tenant_id or str(params.get("tenant_id")) != str(tenant_id):
        return None
    return params, time.time() - body.timestamp


def verify(raw, action_name: str, tenant_id) -> Optional[dict]:
    """The params of signed button data, or None unless it was signed by us for
    this action and this NudgeBee tenant within MAX_AGE_SECONDS."""
    checked = _checked(raw, action_name, tenant_id)
    if not checked or not -300 <= checked[1] <= MAX_AGE_SECONDS:
        return None
    return checked[0]


def expired(raw, action_name: str, tenant_id) -> bool:
    """True for genuine button data that is too old to use."""
    checked = _checked(raw, action_name, tenant_id)
    return bool(checked) and checked[1] > MAX_AGE_SECONDS


def fits(card: dict) -> bool:
    return len(json.dumps(card).encode()) <= CARD_SIZE_LIMIT


# ------------------------------- card content -------------------------------


def suppress_action(finding: dict, tenant_id) -> Optional[dict]:
    """ "Suppress…" for a finding card: a time-boxed suppression of this alert, or
    of every alert with its aggregation key. Each choice is signed on its own,
    since a choice set's value is user input, not part of the button's data."""
    account_id = finding.get("cloud_account_id")
    if not (buttons_enabled() and account_id and tenant_id):
        return None
    fingerprint = str(finding.get("fingerprint") or "").strip()
    alertname = str(finding.get("aggregation_key") or "").strip()

    def choices(scope, durations, label):
        return [
            {
                "title": f"{label} · {text}",
                "value": sign(
                    SUPPRESS,
                    {
                        "event_id": finding.get("id"),
                        "fingerprint": fingerprint,
                        "alertname": alertname,
                        "account_id": account_id,
                        "tenant_id": str(tenant_id),
                        "scope": scope,
                        "duration_hours": hours,
                    },
                ),
            }
            for text, hours in durations
        ]

    options = []
    if fingerprint:
        options += choices("fingerprint", SUPPRESS_THIS_DURATIONS, "This alert")
    if alertname:
        shown = alertname if len(alertname) <= 30 else alertname[:27] + "…"
        options += choices("alertname", SUPPRESS_ALL_DURATIONS, f'All "{shown}" alerts')
    if not options:
        return None
    return {
        "type": "Action.ShowCard",
        "id": SUPPRESS_ACTION_ID,
        "title": "Suppress…",
        "card": {
            "type": "AdaptiveCard",
            "body": [
                {
                    "type": "Input.ChoiceSet",
                    "id": SUPPRESS_CHOICE,
                    "style": "compact",
                    "isRequired": True,
                    "errorMessage": "Pick how long to suppress for",
                    "choices": options,
                }
            ],
            "actions": [{"type": "Action.Submit", "title": "Suppress", "data": {ACTION_KEY: SUPPRESS}}],
        },
    }


def has_actions(card: dict) -> bool:
    return any(a.get("id") == SUPPRESS_ACTION_ID for a in card.get("actions") or [])


def with_status(card: dict, text: Optional[str]) -> dict:
    """The card with its status line set to text, or removed when text is None."""
    body = [item for item in card.get("body") or [] if item.get("id") != STATUS_ID]
    if text:
        body.append({"type": "TextBlock", "id": STATUS_ID, "text": text, "wrap": True, "isSubtle": True})
    return {**card, "body": body}


def without_action(card: dict, action_id: str) -> dict:
    return {**card, "actions": [a for a in card.get("actions") or [] if a.get("id") != action_id]}


# ------------------------------- stored card -------------------------------


def card_row(tenant_id, account_id, fingerprint, channel_id, message_id, card, kind) -> Optional[SentNotifications]:
    """The sent_notifications row for a card with buttons, returned by the sender
    and committed with the rest of the notification. None without a message id,
    since a click finds its card by that id."""
    if not (message_id and channel_id):
        return None
    return SentNotifications(
        created_at=utc_now(),
        tenant_id=tenant_id,
        account_id=account_id or None,
        fingerprint=fingerprint or f"ms_teams:{message_id}",
        teams_channel_id=channel_of(channel_id),
        teams_message_id=message_id,
        teams_metadata=json.dumps({"kind": kind, "card": card, "actions": {}}),
    )


def channel_of(conversation_id: str) -> str:
    """The channel a click came from: its conversation id without the thread suffix."""
    return (conversation_id or "").split(";messageid=")[0]


def _blocking(entry):
    """REFUSED or BUSY when a recorded action state blocks running it again."""
    if not isinstance(entry, dict):
        return None
    if entry.get("state") == DONE:
        return REFUSED
    if entry.get("state") == IN_PROGRESS and time.time() - entry.get("at", 0) < IN_PROGRESS_STALE_SECONDS:
        return BUSY
    return None


def _stamp(card_ref, action, state, mutate: Callable[[dict], dict], refuse_if_started: bool):
    """Lock the card's row, apply mutate to the stored card and record the
    action's state, then commit. card_ref is (tenant_id, channel_id, message_id).
    Returns the new card, None when there is no stored card, or REFUSED / BUSY
    when the action already ran or is running."""
    tenant_id, channel_id, message_id = card_ref
    with Session(sync_engine) as session:
        row = (
            session.query(SentNotifications)
            .filter(
                SentNotifications.tenant_id == tenant_id,
                SentNotifications.teams_channel_id == channel_id,
                SentNotifications.teams_message_id == message_id,
            )
            .with_for_update()
            .first()
        )
        if row is None:
            return None
        meta = json.loads(row.teams_metadata or "{}")
        actions = meta.setdefault("actions", {})
        blocked = _blocking(actions.get(action)) if refuse_if_started else None
        if blocked:
            return blocked
        if state is None:
            actions.pop(action, None)
        else:
            actions[action] = {"state": state, "at": int(time.time())}
        meta["card"] = mutate(meta.get("card") or {})
        row.teams_metadata = json.dumps(meta)
        session.commit()
        return meta["card"]


def begin(card_ref, action, mutate):
    return _stamp(card_ref, action, IN_PROGRESS, mutate, refuse_if_started=True)


def finish(card_ref, action, state, mutate):
    return _stamp(card_ref, action, state, mutate, refuse_if_started=False)


async def _show(activity, card: dict) -> None:
    """Write the stamped card back to Teams. A failure is logged; the outcome
    also goes to the card's thread."""
    try:
        await asyncio.to_thread(
            get_sender().update,
            activity.service_url,
            activity.conversation.id,
            activity.reply_to_id,
            card_activity(card),
        )
    except TeamsBotError as e:
        LOG.warning("Updating Teams card %s failed: %s", activity.reply_to_id, e)


# ------------------------------- clicks -------------------------------


def is_card_action(activity) -> bool:
    return isinstance(activity.value, dict) and bool(activity.value.get(ACTION_KEY))


def _tenant_for(aad_tenant_id):
    with Session(sync_engine) as session:
        return ms_teams_aad_tenant_owner(session, aad_tenant_id, enabled_only=True)


async def handle(common_service, activity, user_email, aad_tenant_id) -> None:
    action = activity.value.get(ACTION_KEY)
    if action == SUPPRESS:
        await _suppress(common_service, activity, user_email, aad_tenant_id)
    else:
        LOG.info("Ignoring unknown Teams card action %s", action)


async def _suppress(common_service, activity, user_email, aad_tenant_id) -> None:
    # Imported here: actions_common imports events, which imports this module.
    from notifications_server.services.actions import validate_and_get_user_tenants
    from notifications_server.services.actions_common import (
        SUPPRESS_NOT_ALLOWED_MESSAGE,
        UNABLE_TO_PROCESS_REQUEST,
        USER_NOT_FOUND_MESSAGE,
        can_manage_triage_rules,
    )

    async def reply(text):
        await common_service.teams_reply(activity, text, aad_tenant_id)

    raw = activity.value.get(SUPPRESS_CHOICE)
    if not raw:
        return await reply(PICK_A_DURATION)
    tenant_id = await asyncio.to_thread(_tenant_for, aad_tenant_id)
    params = verify(raw, SUPPRESS, tenant_id)
    duration_hours = int((params or {}).get("duration_hours") or 0)
    if not (params and params.get("account_id") and params.get("scope") and duration_hours > 0):
        if expired(raw, SUPPRESS, tenant_id):
            return await reply(SUPPRESS_CARD_TOO_OLD)
        LOG.warning("Rejecting a Teams suppress click with invalid data")
        return await reply(UNABLE_TO_PROCESS_REQUEST)
    account_id = params["account_id"]

    user_id, tenants = await asyncio.to_thread(validate_and_get_user_tenants, user_email)
    if not user_id or str(tenant_id) not in {str(t) for t in tenants or []}:
        return await reply(USER_NOT_FOUND_MESSAGE)
    if not await asyncio.to_thread(can_manage_triage_rules, user_id, str(tenant_id), account_id):
        return await reply(SUPPRESS_NOT_ALLOWED_MESSAGE)

    message_id = activity.reply_to_id
    channel_id = channel_of(activity.conversation.id if activity.conversation else "")
    if not (message_id and channel_id):
        LOG.warning("Rejecting a Teams suppress click that doesn't name its card")
        return await reply(UNABLE_TO_PROCESS_REQUEST)
    card_ref = (tenant_id, channel_id, message_id)
    claim = f"{tenant_id}:{message_id}:{SUPPRESS}"
    cache = Cache()
    # The claim covers the action while it runs; once it's done, the stored card
    # refuses repeats.
    if not cache.claim_card_action(claim, CLAIM_SECONDS):
        return await reply(STILL_SUPPRESSING)
    try:
        await _suppress_claimed(activity, reply, card_ref, params, tenant_id, user_id, user_email)
    except Exception as e:
        LOG.exception("Suppressing from Teams failed: %s", e)
        await reply(UNABLE_TO_PROCESS_REQUEST)
    finally:
        cache.release_card_action(claim)


async def _suppress_claimed(activity, reply, card_ref, params, tenant_id, user_id, user_email) -> None:
    """The suppression itself, once the click is checked and claimed."""
    from notifications_server.services.actions_common import (
        SUPPRESS_DURATION_LABELS,
        UNABLE_TO_PROCESS_REQUEST,
        build_suppress_rule_input,
        create_suppress_rule,
    )

    account_id = params["account_id"]
    duration_hours = int(params["duration_hours"])
    started = await asyncio.to_thread(begin, card_ref, SUPPRESS, lambda c: with_status(c, "Suppressing…"))
    if started is REFUSED:
        return await reply(ALREADY_SUPPRESSED)
    if started is BUSY:
        return await reply(STILL_SUPPRESSING)
    if started is not None:
        await _show(activity, started)

    until = datetime.now(timezone.utc) + timedelta(hours=duration_hours)
    rule_input = build_suppress_rule_input(
        account_id,
        params["scope"],
        params.get("fingerprint"),
        params.get("alertname"),
        until,
        f"Suppressed via Microsoft Teams by {user_email}",
    )
    try:
        created = bool(rule_input) and await asyncio.to_thread(
            create_suppress_rule, str(tenant_id), user_id, rule_input
        )
    except Exception as e:
        LOG.warning("Creating a suppression from Teams failed: %s", e)
        created = False
    if not created:
        card = await asyncio.to_thread(finish, card_ref, SUPPRESS, None, lambda c: with_status(c, None))
        if card:
            await _show(activity, card)
        return await reply(UNABLE_TO_PROCESS_REQUEST)

    clicker = (activity.from_property.name if activity.from_property else None) or user_email
    scope_label = "this alert" if params["scope"] == "fingerprint" else f'all "{params.get("alertname")}" alerts'
    duration_label = SUPPRESS_DURATION_LABELS.get(duration_hours, f"{duration_hours}h")
    until_text = until.strftime("%d %b %Y %H:%M UTC")
    status = f"Suppressed ({scope_label} · {duration_label}) by {clicker} · until {until_text}"
    card = await asyncio.to_thread(
        finish,
        card_ref,
        SUPPRESS,
        DONE,
        lambda c: with_status(without_action(c, SUPPRESS_ACTION_ID), status),
    )
    if card:
        await _show(activity, card)
    triage_rules_url = settings.urls.cluster_details_url(
        account_id, utm_source=URLRoutes.UTMSource.TEAMS, anchor=URLRoutes.Anchors.EVENTS_TRIAGE_RULES
    )
    await reply(
        f"{clicker} suppressed {scope_label} for {duration_label}. Repeat alerts won't notify until"
        f" {until_text}. [Open Triage Rules]({triage_rules_url}) to undo."
    )
