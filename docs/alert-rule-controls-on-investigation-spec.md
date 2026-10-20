# Alert-rule controls on the investigation screen — spec

Part of epic #38553. Builds on the phase-1 alert-rule write path
([alert-rule-writes-cloud-prometheus-spec.md](alert-rule-writes-cloud-prometheus-spec.md)).

## Problem

The investigation screen's Remediation tab has no way to act on the alert itself. On
10 dev events reviewed on 2026-09-18, none offered anything about the alert rule, including:

- `OtelDemoGRPCClientErrorRate` — 831 firings in 7 days. Nubi's plan concludes the rule is a
  false positive (it should exclude `rpc_method="EventStream"`) and then says "no direct CLI
  action is proposed", although the rule is an unmanaged PrometheusRule Nudgebee can edit.
- `report_crash_loop` (built-in finding) — 518 firings in 7 days across 38 workloads, 177 of
  them from one deliberately broken test deployment.

Muting today exists only in Alert Manager and only for a whole rule. For a built-in finding a
whole-rule mute silences every workload in the cluster.

## Decisions (from /challenge, REVISE round 1)

- **D1. A mute is not a remediation.** It is never written to `event_resolution`. The
  recurrence check counts events by fingerprint without looking at `nb_status`, so a mute
  recorded as a remediation would read "Recurred N times since — this did not stop it" the
  next day. The row shows live mute state instead.
- **D2. Scopes are built from the event, never from a fingerprint.** Fingerprints never
  repeat for Azure, Elasticsearch, anomaly and automation events, so a fingerprint mute
  silently matches nothing there. Alert keys (`aggregation_key`) repeat for 94–100% of events
  in every source on dev, so key-based mutes keep matching.
- **D3. One mute model.** Every mute is an ordinary triage suppression rule. `action_value`
  carries `mute_event_rule_id` (when the event resolves to a rule) and a new `mute_scope`
  (`rule` | `workload`). Alert Manager reads both.
- **D4. Upstream changes stay where they are.** Disable/enable at source reuses the existing
  eventrule path and its confirm dialog (with the Helm warning). Edit links to the rule's
  Alert Manager row. Nothing about them is recorded as a remediation either.

Round 2 (fresh-context adversary) — PROCEED with these folded in:

- **D5. A muted firing is not a false positive.** Every suppressed firing gets an
  `event_classifications` row (`false_positive`, `known_noise`) and a history line
  (`triage/rules.go:455-478`); today's Alert Manager mute already does this. When the winning
  suppression rule is a mute, the row uses `reason_code = "muted"` so the alert-quality work
  and analytics can tell a mute from a confirmed false positive.
- **D6. Rule lookup prefers the definition over copies.** A Prometheus alert can have a
  `prometheus` row and a `prometheus_alertmanager_webhook` copy (33 alerts on dev), and a
  `pagerduty_webhook` event maps to both. Candidates are ranked: rows Nudgebee can write
  (`prometheus`, `prometheus_user`, AWS/GCP/Azure) first; exactly one → resolved. If there is
  none, exactly one copy row → resolved as a copy. Otherwise no rule.
- **D7. Workload scope needs both namespace and owner.** Webhook subject resolution sets the
  owner but leaves the namespace empty when the name is ambiguous
  (`integrations/core/subject_resolution.go:189-211`); an owner-only mute would silence that
  workload name in every namespace.
- **D8. No duplicate mute rows.** Mute writes take a transaction-scoped advisory lock on
  (account, marker, scope, namespace, owner) before the select-then-insert.
- **D9. Show every suppression that applies, remove only mutes.** `active_mutes` lists any
  enabled, unexpired suppression rule matching the event. Mutes get Unmute; other rules (e.g.
  from "classify as false positive") show their name and a link to Triage Rules, so "Unmute"
  never leaves the event silently suppressed.
- **D10. The marker is set whenever the event maps to an `event_rules` row**, even one whose
  rule-level Mute is not offered (datadog, built-in checks), so Alert Manager shows the mute
  and offers Unmute for it.

## Scopes

Worked out on the server per event. Counts are events in the last 7 days that the scope
would have matched, so the user sees how much it silences before confirming.

| Scope | Label | Match | Offered when |
|---|---|---|---|
| `workload` | "Only `<ns>/<owner>`" | alert key + `match_namespace` (exact) + `match_service` = anchored owner | the event has both `subject_namespace` and `subject_owner` (D7) |
| `rule` | "Every `<alert>` alert" (+ "N workloads") | the existing rule mute (all delivery sources) when the event resolves to a rule whose Mute is available; otherwise the event's own source + anchored alert key | always |

Every mute has an end: 24 hours or 7 days (default), or "until I unmute". There is no
fingerprint scope.

## Rule lookup for an event

Event → `event_rules` row, source-aware: candidate rule sources are the event's own source
plus those whose `MuteEventSources` include it (so a `pagerduty_webhook` event finds the
`prometheus` rule), with `alert = aggregation_key` and the same account, ranked per D6.
Resolved → its capabilities. No rule → only event-built mutes are offered.

## API (api-server, services-server RPC)

- `eventrules_get_event_alert_controls` `{event_id, account_id}` →
  `{alert, source, rule: capabilities|null, rule_location, scopes: [{scope, namespace, owner,
  matched_7d, workloads_7d, muted, expires_at}], suppressions: [{rule_id, name, is_mute,
  mute_scope, expires_at}]}`. `suppressions` = every enabled, unexpired suppression rule that
  matches the event (D9).
- `eventrules_update_event_mute` `{event_id, account_id, scope, duration_hours (24|168|0),
  muted}` → the fresh controls. Unmute disables the rows of that scope only (rule scope: the
  rule-wide rows; workload scope: rows with the same namespace/owner).

The read checks account read access; the mute checks update access like
`eventrules_update_mute` and writes an audit entry.

## Alert Manager changes

- `muted` becomes "a rule-scope mute is active and unexpired" (today it ignores
  `effective_until`, so an expired mute still shows Muted).
- New `muted_workloads` count; status reads "Enabled · Muted for 2 workloads".
- Unmute in Alert Manager disables every mute tied to the rule, any scope (unchanged
  behaviour for rule scope, now stated).
- The existing rule mute must not re-purpose a workload-scope row: its "reuse an earlier row"
  lookup filters on scope.

## UI (investigation screen, Remediation tab)

A section **"Stop this alert"** between "What you can do" and "Run an automation", kept to
one quiet row so it never outweighs a real fix:

- Left: the alert name and where it is defined ("PrometheusRule victoria/otel-demo-alerts · from
  the agent chart", "GCP alert policy", "Built-in Nudgebee check"); under it, one small line per
  active mute ("Muted for demo/fraud-detection · ends in 23h · Unmute") and per other
  suppression rule ("Already suppressed by the triage rule … · Manage").
- Right: **Mute…** (dialog: which alerts, with 7-day counts; for how long; one line on what a
  mute does) and a **⋯** menu with **Turn off / on where it is defined…** (same confirm and
  warning as Alert Manager) and **Edit in Alert Manager**, each shown only when the capability is
  available.
- Unavailable actions are hidden, not greyed out. Needs write access, like the other rows.
- "No fixes are available for this event yet." is not shown when this section is.

## Not in this change

- Moving the section to the top when triage's alert-quality verdict says the alert is noise
  (`false_positive` / `noisy_but_real`). Needs the threshold-suggestion call, which is expensive;
  separate PR.
- Feeding the capabilities into Nubi's remediation plan ("edit this rule" as a typed action).
- Regex namespace scopes (`nb-repro-*`); `match_namespace` is exact today.
- Muted events still join incident groups (`triage/processor.go` only skips `drop`).

## Acceptance criteria (dev, local api-server + local app)

1. **Otel false positive** (event `c4c04989`): section shows the rule and where it lives;
   scopes "Only demo/fraud-detection — 424 in 7 days" and "Every OtelDemoGRPCClientErrorRate
   alert — 831, 2 workloads"; Disable at source and Edit shown.
2. **Built-in crash loop** (event `8bf5a12e`): scopes "Only benchmark-scenarios/payment-worker
   — 177" and "Every Report Crash Loop alert — 518, 38 workloads"; no Disable, no Edit.
3. **Cloud** (GCP event `1744f36e`): no workload scope; rule scope uses the rule mute;
   Disable at source shown.
4. **Workload mute, 24 h**: creates suppression rows with namespace/owner and
   `effective_until` ≈ now + 24 h; after the dev triage pods' rule cache refreshes (≤ 60 s —
   a local api-server clears only its own cache), the next firing for that workload is
   SUPPRESSED with a `muted` classification, other workloads' firings are not; Alert Manager
   shows "Muted for 1 workload"; Unmute from the event removes it. (A muted firing is no
   longer also marked DUPLICATE: only the winning rule type applies.)
5. **No remediation record**: after mute and unmute, the event's History and the
   recurrence line are unchanged.
6. Unit tests: expired mutes are not reported as muted; a rule mute after a workload mute
   leaves the workload row's namespace/owner intact; scope building for events with and
   without owner/namespace; source-aware rule lookup (pagerduty event → prometheus rule, and
   → prometheus row when a webhook copy also exists); mute suppression writes
   `reason_code = muted`.

## Verified on dev (2026-09-18, local api-server + app against dev)

- Workload mute of `OtelDemoGRPCClientErrorRate` for `demo/fraud-detection`: the next two
  firings for that workload arrived SUPPRESSED; the same alert on `ad` did not. Unmute
  disabled all four rows. The event has no `event_resolution` row.
- Mute and unmute from the event for every source kind: Prometheus via PagerDuty (rule-wide
  covers prometheus, alertmanager, pagerduty, zenduty), built-in crash loop (workload),
  Datadog, GCP, AWS (CloudWatch + EventBridge), Azure. Alert Manager showed each, including
  "Muted for 1 workload" and Unmute for rules whose own Mute is not offered.
- Turn off / on where defined: the Otel PrometheusRule lost only that alert and got it back
  identical; the GCP policy went off and on with its channels intact.
- Found and fixed: Azure log / activity-log alerts (`alert_type` ≠ `metric`) were offered
  Disable at source, which cloud-collector cannot do ("metric alert not found"). They are now
  mute-only.
- Not verifiable on dev: an Azure metric-alert toggle. The synced metric alerts no longer exist
  in Azure (ResourceNotFound); the sync does not remove deleted alerts.
