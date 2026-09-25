"""
The migration orders existing rules so the ordered engine reproduces the old
"any rule wins" combination. This compares the two over random rule sets that
all match one event. The only allowed differences are the two decided ones:
several matched rules mapping one platform to different channels now all receive
the message, and a suppress rule or active snooze now also stops email.
`old_outcome` is a frozen copy of the combination the ordered engine replaced.
"""

import random
from datetime import datetime, timedelta

from notifications_server.services.message import resolve_outcome

NOW = datetime.utcnow()
PLATFORMS = ["slack", "ms_teams"]
CHANNELS = ["#a", "#b", "#c"]


def random_rules(rng):
    rules = []
    for i in range(rng.randint(1, 5)):
        snooze = rng.choice([None, None, NOW + timedelta(days=1), NOW - timedelta(days=1)])
        suppressed = rng.random() < 0.2
        mappings = []
        if not suppressed:
            for platform in PLATFORMS:
                if rng.random() < 0.5:
                    mappings.append((platform, rng.choice(CHANNELS)))
            if rng.random() < 0.3:
                mappings.append(("email", rng.choice(["ops@x", "dev@x"])))
        rules.append(
            dict(
                id=f"r{i}",
                created=i,
                suppressed=suppressed,
                expires_at=snooze,
                batch=rng.random() < 0.1,
                mappings=mappings,
            )
        )
    return rules


def entries(rules, continue_of):
    out = []
    for r in rules:
        base = dict(
            rule_id=r["id"],
            suppressed=r["suppressed"],
            expires_at=r["expires_at"],
            delivery_mode="batch" if r["batch"] else "real_time",
            severity=None,
            continue_matching=continue_of(r),
        )
        if r["mappings"]:
            for platform, channel in r["mappings"]:
                out.append(dict(base, platform=platform, channels={"id": channel}))
        else:
            out.append(dict(base, platform=None, channels=None))
    return out


def old_outcome(rules):
    """main: any suppress / active snooze / batch wins; chat per platform = first
    matched rule's mapping (creation order), else the default channel; email = union
    of every matched rule's recipients plus the task's own, ignoring suppression."""
    email = {c for r in rules for p, c in r["mappings"] if p == "email"} | {"task@x"}
    if any(r["suppressed"] for r in rules):
        return "suppressed", {}, email
    if any(r["expires_at"] and r["expires_at"] > NOW for r in rules):
        return "snoozed", {}, email
    if any(r["batch"] for r in rules):
        return "queued", {}, email
    chat = {}
    for platform in PLATFORMS:
        chosen = next((c for r in rules for p, c in r["mappings"] if p == platform), "default")
        chat[platform] = {chosen}
    return "deliver", chat, email


def migrated_order(rules):
    def group(r):
        if r["suppressed"]:
            return 1
        if r["expires_at"] is not None:
            return 2
        return 3

    def latest_expiry_first(r):
        return -(r["expires_at"].timestamp()) if r["expires_at"] is not None else 0

    return sorted(rules, key=lambda r: (group(r), latest_expiry_first(r), r["created"]))


def new_outcome(rules):
    ordered = migrated_order(rules)
    matched = entries(ordered, continue_of=lambda r: not r["suppressed"])
    outcome = resolve_outcome(matched, source="troubleshoot")
    email_outcome = resolve_outcome(matched, source="troubleshoot", honour_batch=False)
    if email_outcome.response is not None:
        email = set()
    else:
        email = {t["channels"]["id"] for t in email_outcome.targets if t["platform"] == "email"}
        if email_outcome.use_defaults:
            email |= {"task@x"}
    if outcome.response is not None:
        return outcome.response[0]["status"], {}, email
    chat = {}
    for platform in PLATFORMS:
        channels = {t["channels"]["id"] for t in outcome.targets if t["platform"] == platform}
        if not channels and outcome.use_defaults:
            channels = {"default"}
        if channels:
            chat[platform] = channels
    return "deliver", chat, email


def _has_stop_rule(rules):
    return any(r["suppressed"] or (r["expires_at"] and r["expires_at"] > NOW) for r in rules)


def _classify(rules, old, new):
    if old == new:
        return "same"
    if old[0] != new[0] or not all(old[1][p] <= new[1].get(p, set()) for p in old[1]):
        return "other"
    if old[2] == new[2]:
        return "chat_union"
    if new[2] < old[2] and _has_stop_rule(rules):
        return "email_stopped"
    return "other"


def test_migrated_order_reproduces_any_rule_wins_except_the_decided_differences():
    rng = random.Random(7)
    unexplained = []
    seen = set()
    for _ in range(3000):
        rules = random_rules(rng)
        kind = _classify(rules, old_outcome(rules), new_outcome(rules))
        seen.add(kind)
        if kind == "other":
            unexplained.append(rules)
    assert unexplained == []
    assert {"same", "chat_union", "email_stopped"} <= seen
