"""Read-only report of the notification rules whose outcome changes when rules are
evaluated in order instead of "any matching rule wins".

After the migration orders existing rules, delivery is unchanged except where two
rules that can match the same event:
  extra_copies:  both deliver to one chat platform, on different channels. Both
                 channels now receive the message; before, only one did.
  email_stopped: one suppresses (or is snoozed) and the other has email recipients.
                 The email is now stopped; before, it was still sent.

Usage: DATABASE_URL=postgresql://... python scripts/rule_order_report.py
"""

import itertools
import json
import os
import sys
from collections import defaultdict
from datetime import datetime, timezone

import psycopg2
import psycopg2.extras

WEBHOOK_PLATFORMS = {"ms_teams_webhook", "slack_webhook", "google_chat_webhook"}
NARROWING_FIELDS = ("namespace", "workload", "aggregation_key", "severity")


def load(conn):
    with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
        cur.execute("""
            SELECT r.id::text, r.tenant_id::text, r.source, r.name, r.scope_mode, r.is_suppressed,
                   r.expires_at, r.namespace, r.workload, r.aggregation_key, r.severity, r.event_types,
                   COALESCE(array_agg(a.account_id::text) FILTER (WHERE a.account_id IS NOT NULL), '{}') AS accounts
            FROM notification_rules r
            LEFT JOIN notification_rule_accounts a ON a.rule_id = r.id
            WHERE COALESCE(r.is_active, true)
            GROUP BY r.id
            """)
        rules = {row["id"]: dict(row, mappings=[]) for row in cur.fetchall()}
        cur.execute("SELECT rule_id::text, platform, channels FROM notification_rule_mappings")
        for row in cur.fetchall():
            if row["rule_id"] in rules:
                rules[row["rule_id"]]["mappings"].append((row["platform"], row["channels"]))
    return list(rules.values())


def _event_types(rule):
    value = rule["event_types"]
    if isinstance(value, str):
        value = json.loads(value)
    return set(value or [])


def can_both_match(a, b):
    if a["scope_mode"] == "accounts" and b["scope_mode"] == "accounts":
        if not set(a["accounts"]) & set(b["accounts"]):
            return False
    for key in NARROWING_FIELDS:
        if a[key] is not None and b[key] is not None and a[key] != b[key]:
            return False
    if a["source"] == "automation" and not _event_types(a) & _event_types(b):
        return False
    return True


def _is_stop(rule, now):
    return bool(rule["is_suppressed"]) or (rule["expires_at"] is not None and rule["expires_at"] > now)


def _chat_channels(rule):
    channels = defaultdict(set)
    for platform, value in rule["mappings"]:
        if platform not in WEBHOOK_PLATFORMS and platform != "email" and value:
            channels[platform].add(json.dumps(value, sort_keys=True, default=str))
    return channels


def _has_email(rule):
    return any(platform == "email" and value for platform, value in rule["mappings"])


def report(rules, now):
    findings = []
    by_source = defaultdict(list)
    for rule in rules:
        by_source[(rule["tenant_id"], rule["source"])].append(rule)
    for (tenant, source), group in sorted(by_source.items()):
        for a, b in itertools.combinations(group, 2):
            if not can_both_match(a, b):
                continue
            if not _is_stop(a, now) and not _is_stop(b, now):
                chat_a, chat_b = _chat_channels(a), _chat_channels(b)
                for platform in chat_a.keys() & chat_b.keys():
                    if chat_a[platform] != chat_b[platform]:
                        findings.append((tenant, source, "extra_copies", platform, a["name"], b["name"]))
            for stop, other in ((a, b), (b, a)):
                if _is_stop(stop, now) and not _is_stop(other, now) and _has_email(other):
                    findings.append((tenant, source, "email_stopped", "email", stop["name"], other["name"]))
    return findings


def main():
    url = os.environ.get("DATABASE_URL")
    if not url:
        sys.exit("DATABASE_URL is required")
    conn = psycopg2.connect(url)
    conn.set_session(readonly=True)
    try:
        rules = load(conn)
    finally:
        conn.close()
    now = datetime.now(timezone.utc).replace(tzinfo=None)
    findings = report(rules, now)
    tenants = defaultdict(lambda: defaultdict(int))
    for tenant, _source, kind, *_ in findings:
        tenants[tenant][kind] += 1
    print(f"rules={len(rules)} findings={len(findings)} tenants_affected={len(tenants)}")
    for tenant, counts in sorted(tenants.items()):
        print(f"tenant {tenant}: " + ", ".join(f"{kind}={count}" for kind, count in sorted(counts.items())))
    for finding in findings:
        print("\t".join(str(value) for value in finding))


if __name__ == "__main__":
    main()
