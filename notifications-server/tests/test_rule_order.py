"""
Rules of one source are evaluated in order: the first matching rule decides,
unless it continues matching. A suppress rule or an active snooze stops the walk
on every channel, email included; copies collected before the stop still go out.
Batch rules stay outside the order. Rows written without an order keep the old
meaning, where any matching suppress or snooze wins.
"""

import asyncio
import uuid
from datetime import datetime, timedelta
from types import SimpleNamespace

import pytest

from notifications_server.models.models import NotificationRules
from notifications_server.processors.notification import NotificationProcessor
from notifications_server.services import rules as rules_module
from notifications_server.services.message import (
    MessageService,
    NotificationRuleMatcher,
    resolve_outcome,
    send_in_rounds,
)
from notifications_server.services.rules import NotificationRulesService, _is_broader, rule_order_key

TENANT = "22222222-2222-2222-2222-222222222222"
NOW = datetime.utcnow()


def _entry(rule_id, platform="slack", channel=None, suppressed=False, expires_at=None, continues=False, batch=False):
    return {
        "rule_id": rule_id,
        "suppressed": suppressed,
        "platform": platform,
        "channels": {"id": channel or f"#{rule_id}"} if platform else None,
        "expires_at": expires_at,
        "delivery_mode": "batch" if batch else "real_time",
        "severity": None,
        "continue_matching": continues,
    }


def _targets(outcome):
    return [(entry["platform"], entry["channels"]["id"]) for entry in outcome.targets]


# ----------------------------- resolving an outcome -----------------------------


def test_a_narrow_rule_above_a_broad_suppress_delivers():
    outcome = resolve_outcome([_entry("prod", channel="#prod"), _entry("all", platform=None, suppressed=True)])
    assert outcome.response is None
    assert _targets(outcome) == [("slack", "#prod")]
    assert outcome.deciding_rule_id == "prod"


def test_a_broad_suppress_above_a_narrow_rule_suppresses():
    outcome = resolve_outcome([_entry("all", platform=None, suppressed=True), _entry("prod")])
    assert outcome.response[0]["status"] == "suppressed"
    assert outcome.targets == []


def test_continue_collects_every_later_rule_until_one_decides():
    outcome = resolve_outcome(
        [_entry("audit", channel="#audit", continues=True), _entry("prod", channel="#prod"), _entry("late")]
    )
    assert _targets(outcome) == [("slack", "#audit"), ("slack", "#prod")]
    assert outcome.use_defaults is True


def test_a_copy_above_a_suppress_still_goes_out_without_defaults():
    outcome = resolve_outcome(
        [_entry("audit", channel="#audit", continues=True), _entry("all", platform=None, suppressed=True)]
    )
    assert outcome.response is None
    assert _targets(outcome) == [("slack", "#audit")]
    assert outcome.use_defaults is False


def test_an_active_snooze_stops_and_an_expired_one_delivers():
    later = NOW + timedelta(days=1)
    active = resolve_outcome([_entry("snooze", expires_at=later, continues=True), _entry("prod")])
    assert active.response[0]["status"] == "snoozed"

    earlier = NOW - timedelta(days=1)
    expired = resolve_outcome(
        [_entry("snooze", channel="#snooze", expires_at=earlier, continues=True), _entry("prod", channel="#prod")]
    )
    assert _targets(expired) == [("slack", "#snooze"), ("slack", "#prod")]


def test_batch_anywhere_queues_and_never_contributes_targets():
    outcome = resolve_outcome([_entry("prod", continues=True), _entry("digest", batch=True)])
    assert outcome.response[0]["status"] == "queued"
    # Automation messages cannot be queued: the digest is built from events.
    assert resolve_outcome([_entry("digest", batch=True)], source="automation").response is None
    # Email tasks ignore batch rules.
    assert resolve_outcome([_entry("digest", batch=True)], honour_batch=False).response is None


def test_approvals_skip_stop_rules():
    outcome = resolve_outcome(
        [_entry("all", platform=None, suppressed=True), _entry("ops", channel="#ops")],
        source="automation",
        event_type="automation.approval.requested",
    )
    assert outcome.response is None
    assert _targets(outcome) == [("slack", "#ops")]


def test_no_match_uses_the_defaults():
    outcome = resolve_outcome([])
    assert (outcome.response, outcome.targets, outcome.use_defaults) == (None, [], True)


def test_a_rule_without_mappings_decides_but_leaves_defaults():
    outcome = resolve_outcome([_entry("prod", platform=None), _entry("late", channel="#late")])
    assert outcome.targets == []
    assert outcome.use_defaults is True


def test_the_effect_wrapper_keeps_its_old_signature():
    matched = [_entry("all", platform=None, suppressed=True)]
    assert MessageService._maybe_return_rule_effect(matched, TENANT)[0]["status"] == "suppressed"
    assert MessageService._maybe_return_rule_effect([], TENANT) is None


# ----------------------------- evaluation order -----------------------------


def _rule(rule_id, position=None, suppressed=False, expires_at=None, continues=None, created=0, **overrides):
    base = dict(
        id=rule_id,
        source="troubleshoot",
        account_id=None,
        scope_mode="all",
        account_ids=[],
        missing_columns=frozenset(),
        namespace=None,
        workload=None,
        aggregation_key=None,
        severity=None,
        is_active=True,
        is_suppressed=suppressed,
        expires_at=expires_at,
        delivery_mode="real_time",
        event_types=None,
        position=position,
        continue_matching=continues,
        created_at=NOW + timedelta(seconds=created),
    )
    base.update(overrides)
    return SimpleNamespace(**base)


def test_order_is_by_position_then_creation():
    rules = [_rule("c", position=3000), _rule("a", position=1000), _rule("b", position=1000, created=5)]
    assert [r.id for r in sorted(rules, key=rule_order_key)] == ["a", "b", "c"]


def test_an_unordered_snooze_only_goes_first_while_it_is_active():
    active = _rule("active-snooze", expires_at=NOW + timedelta(days=1))
    expired = _rule("expired-snooze", expires_at=NOW - timedelta(days=1))
    positioned = _rule("positioned-suppress", position=1000, suppressed=True)
    order = [r.id for r in sorted([expired, positioned, active], key=rule_order_key)]
    assert order == ["active-snooze", "positioned-suppress", "expired-snooze"]


def test_unordered_rows_keep_any_suppress_wins():
    rules = [
        _rule("ordered-deliver", position=1000),
        _rule("legacy-deliver"),
        _rule("legacy-suppress", suppressed=True),
    ]
    assert [r.id for r in sorted(rules, key=rule_order_key)] == ["legacy-suppress", "ordered-deliver", "legacy-deliver"]


class _Mapping:
    def __init__(self, rule_id, channel):
        self.rule_id = rule_id
        self.platform = "slack"
        self.channels = {"id": channel}


class _MappingSession:
    def __init__(self, mappings):
        self._mappings = mappings

    async def execute(self, _statement):
        mappings = self._mappings
        return SimpleNamespace(scalars=lambda: SimpleNamespace(all=lambda: mappings))


class _RulesService:
    def __init__(self, rules):
        self._rules = rules

    async def get_notification_rules(self, _tenant_id):
        return self._rules


def _match(rules, mappings):
    payload = {"source": "troubleshoot", "parameters": {"finding": {"cloud_account_id": "acc-1"}}}
    matcher = NotificationRuleMatcher(_RulesService(rules))
    matched, _ = asyncio.run(matcher.match_notification_rules(_MappingSession(mappings), TENANT, "finding", payload))
    return matched


def test_the_matcher_returns_rules_in_order_with_their_continue_flag():
    rules = [
        _rule("broad", position=2000, suppressed=True, continues=False),
        _rule("prod", position=1000, continues=False),
    ]
    matched = _match(rules, [_Mapping("prod", "#prod")])
    assert [(m["rule_id"], m["continue_matching"]) for m in matched] == [("prod", False), ("broad", False)]
    assert resolve_outcome(matched).deciding_rule_id == "prod"


def test_an_unordered_deliver_rule_continues():
    matched = _match([_rule("legacy")], [_Mapping("legacy", "#legacy")])
    assert matched[0]["continue_matching"] is True


# ----------------------------- channels -----------------------------


def _installation(platform="slack", default="#default"):
    return SimpleNamespace(platform=platform, channels={"id": default}, to_channel=None, id=uuid.uuid4())


def test_every_target_on_a_platform_gets_the_message():
    ip = _installation()
    targets = [_entry("a", channel="#a"), _entry("b", channel="#b"), _entry("c", channel="#a")]
    MessageService.get_channels_from_request_or_defaults([ip], targets, "troubleshoot", {})
    assert ip.to_channel == {"id": "#a"}
    assert ip.additional_channels == [{"id": "#b"}]


def test_defaults_only_when_the_outcome_allows_them():
    ip = _installation()
    MessageService.get_channels_from_request_or_defaults([ip], [], "troubleshoot", {}, use_defaults=False)
    assert ip.to_channel is None
    MessageService.get_channels_from_request_or_defaults([ip], [], "troubleshoot", {})
    assert ip.to_channel == {"id": "#default"}


def test_rounds_send_each_channel_through_the_same_installation():
    ip = _installation()
    ip.to_channel = {"id": "#a"}
    ip.additional_channels = [{"id": "#b"}, {"id": "#c"}]
    sent = []

    async def send(platforms):
        sent.extend((p is ip, p.to_channel["id"]) for p in platforms)
        return [{"status": "success"}]

    responses = asyncio.run(send_in_rounds([ip], send))
    assert sent == [(True, "#a"), (True, "#b"), (True, "#c")]
    assert len(responses) == 3


# ----------------------------- email -----------------------------


class _Matcher:
    def __init__(self, matched):
        self._matched = matched

    async def match_notification_rules(self, *_args):
        return self._matched, "daily_recap"


class _NoSession:
    def __call__(self):
        return self

    async def __aenter__(self):
        return None

    async def __aexit__(self, *exc):
        return False


def _email_entry(rule_id, emails, continues=False):
    entry = _entry(rule_id, platform="email", continues=continues)
    entry["channels"] = {"emails": emails}
    return entry


def _email_recipients(monkeypatch, matched):
    from notifications_server.processors import notification as processor_module

    monkeypatch.setattr(processor_module.BaseDB, "async_session", staticmethod(lambda engine: _NoSession()))
    processor = NotificationProcessor.__new__(NotificationProcessor)
    processor.engine = None
    processor._rule_matcher = _Matcher(matched)
    return asyncio.run(processor._get_configured_email_rules(TENANT, {"source": "daily_recap"}))


def test_a_suppress_rule_stops_email(monkeypatch):
    assert _email_recipients(monkeypatch, [_entry("all", platform=None, suppressed=True)]) is None


def test_email_comes_from_the_rules_that_receive_the_message(monkeypatch):
    matched = [
        _email_entry("copy", ["audit@example.com"], continues=True),
        _entry("all", platform=None, suppressed=True),
        _email_entry("below", ["late@example.com"]),
    ]
    emails, exclusions, use_defaults = _email_recipients(monkeypatch, matched)
    assert emails == ["audit@example.com"]
    # The stop means the task's own recipients are not added.
    assert use_defaults is False


def test_email_without_rules_keeps_the_task_recipients(monkeypatch):
    assert _email_recipients(monkeypatch, []) == ([], [], True)


# ----------------------------- placement and reorder -----------------------------


def _db_rule(name, position, scope_mode="all", **fields):
    return NotificationRules(
        id=uuid.uuid4(),
        tenant_id=TENANT,
        name=name,
        source="troubleshoot",
        is_active=True,
        is_suppressed=fields.pop("is_suppressed", False),
        scope_mode=scope_mode,
        position=position,
        continue_matching=fields.pop("continue_matching", False),
        created_at=NOW,
        **fields,
    )


@pytest.mark.parametrize(
    "broad, new_fields, scope, accounts, expected",
    [
        (dict(scope_mode="all"), {}, "accounts", ["a"], True),
        (dict(scope_mode="accounts", accounts=["a", "b"]), {}, "accounts", ["a"], True),
        (dict(scope_mode="accounts", accounts=["a"]), {}, "accounts", ["a", "b"], False),
        (dict(scope_mode="accounts", accounts=["a"]), {}, "all", [], False),
        (dict(scope_mode="all", namespace="payments"), {"namespace": "payments"}, "all", [], True),
        (dict(scope_mode="all", namespace="payments"), {}, "all", [], False),
        (dict(scope_mode="all", event_types=["x", "y"]), {"event_types": ["x"]}, "all", [], True),
        (dict(scope_mode="all", event_types=["x"]), {"event_types": ["x", "y"]}, "all", [], False),
        (dict(scope_mode="all", event_types=["x"]), {}, "all", [], False),
    ],
)
def test_broader_means_it_matches_everything_the_new_rule_matches(broad, new_fields, scope, accounts, expected):
    broad = dict(broad)
    rule_accounts = broad.pop("accounts", [])
    rule = SimpleNamespace(
        scope_mode=broad.pop("scope_mode"),
        namespace=broad.get("namespace"),
        workload=None,
        aggregation_key=None,
        severity=None,
        event_types=broad.get("event_types"),
    )
    assert _is_broader(rule, rule_accounts, new_fields, scope, accounts) is expected


class _OrderSession:
    """Answers the ordering queries against an in-memory list of rules."""

    def __init__(self, rules):
        self.rules = rules
        self.added = []
        self.committed = False

    async def execute(self, statement, params=None):
        sql = str(statement)
        if "pg_advisory_xact_lock" in sql:
            return None
        if "FROM notification_rule_accounts" in sql:
            return SimpleNamespace(all=lambda: [])
        rules = self.rules + [o for o in self.added if isinstance(o, NotificationRules)]
        return SimpleNamespace(scalars=lambda: SimpleNamespace(all=lambda: rules))

    def add(self, obj):
        self.added.append(obj)

    async def flush(self):
        pass

    async def commit(self):
        self.committed = True


def _order_service(monkeypatch, session):
    class _Factory:
        def __call__(self):
            return self

        async def __aenter__(self):
            return session

        async def __aexit__(self, *exc):
            return False

    monkeypatch.setattr(rules_module.BaseDB, "async_session", staticmethod(lambda engine: _Factory()))
    monkeypatch.setattr(rules_module, "create_audit_request", lambda **kwargs: None)
    service = NotificationRulesService(engine=None)
    service.cache = SimpleNamespace(redis_client=None)
    return service


def test_a_new_narrow_rule_lands_above_the_broad_rule_it_carves_from(monkeypatch):
    narrow_existing = _db_rule("prod only", 1000, scope_mode="accounts", namespace="payments")
    broad = _db_rule("silence all", 2000, is_suppressed=True)
    tail = _db_rule("tail", 3000, namespace="other")
    session = _OrderSession([narrow_existing, broad, tail])
    service = _order_service(monkeypatch, session)
    new_rule = _db_rule("new", None, scope_mode="accounts")
    session.added.append(new_rule)

    asyncio.run(service._place_new_rule(session, TENANT, "troubleshoot", new_rule.id, {}, "accounts", ["acc"]))

    order = sorted(session.rules + [new_rule], key=rule_order_key)
    assert [r.name for r in order] == ["prod only", "new", "silence all", "tail"]
    assert [r.position for r in order] == [1000, 2000, 3000, 4000]


def test_a_new_rule_with_nothing_broader_goes_last(monkeypatch):
    existing = _db_rule("prod only", 1000, scope_mode="accounts")
    session = _OrderSession([existing])
    service = _order_service(monkeypatch, session)
    new_rule = _db_rule("new", None)
    session.added.append(new_rule)

    asyncio.run(service._place_new_rule(session, TENANT, "troubleshoot", new_rule.id, {}, "all", []))

    assert (existing.position, new_rule.position) == (1000, 2000)


def _reorder(service, rule_ids):
    return asyncio.run(
        service.update_rule_order(
            action={"name": "notifications_update_rule_order"},
            input={"request": {"source": "troubleshoot", "rule_ids": rule_ids}},
            session_variables={"tenant_id": TENANT, "user_id": str(uuid.uuid4())},
        )
    )


def test_reorder_writes_the_given_order(monkeypatch):
    a, b, c = _db_rule("a", 1000), _db_rule("b", 2000), _db_rule("c", 3000)
    session = _OrderSession([a, b, c])
    service = _order_service(monkeypatch, session)

    result = _reorder(service, [str(c.id), str(a.id), str(b.id)])

    assert "error" not in result, result
    assert (c.position, a.position, b.position) == (1000, 2000, 3000)
    assert session.committed


@pytest.mark.parametrize("drop_or_duplicate", ["drop", "duplicate"])
def test_reorder_rejects_a_stale_list(monkeypatch, drop_or_duplicate):
    a, b = _db_rule("a", 1000), _db_rule("b", 2000)
    session = _OrderSession([a, b])
    service = _order_service(monkeypatch, session)
    rule_ids = [str(a.id)] if drop_or_duplicate == "drop" else [str(a.id), str(a.id), str(b.id)]

    result = _reorder(service, rule_ids)

    assert result["status_code"] == 409
    assert (a.position, b.position) == (1000, 2000)
    assert not session.committed


def test_a_cached_rule_keeps_its_order():
    rule = _db_rule("a", 2000, continue_matching=True)
    restored = NotificationRules.from_dict(rule.to_dict())
    assert (restored.position, restored.continue_matching, restored.created_at) == (2000, True, rule.created_at)
    assert rule_order_key(restored) == rule_order_key(rule)


def test_an_update_that_omits_ordering_keeps_it():
    rule = _db_rule("a", 2000, continue_matching=False)
    NotificationRulesService._update_existing_rule(rule, {"name": "a", "source": "troubleshoot"}, str(uuid.uuid4()))
    assert (rule.position, rule.continue_matching) == (2000, False)
