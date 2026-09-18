# Nubi recommends and applies alert-rule fixes — spec

Part of epic #38553. Builds on the alert-rule write path and the investigation screen's
"Stop this alert" controls ([alert-rule-controls-on-investigation-spec.md](alert-rule-controls-on-investigation-spec.md)).

## Problem

Nubi's investigations already conclude, in prose, that an alert rule itself is wrong, and say
how to fix it. In production over 30 days, 99 of 2,679 detailed reports (3.7%) did, e.g.:

- `PVCPendingMissingStorageClass` (44 reports): "overbroad PromQL
  `kube_persistentvolumeclaim_status_phase{phase="Pending"} == 1` … add a `for` clause / check
  the storage class". The rule lives in `nudgebee-prometheus.rules`, which Nudgebee edits
  directly.
- `OtelDemoGRPCClientErrorRate`: "filter out EventStream". Nubi's remediation plan then says
  "no direct CLI action is proposed", although the rule is an unmanaged PrometheusRule that
  Nudgebee can edit.

The remediation plan ("Generate Remediation", generated on click) has no way to act on this:
its actions are shell commands run through the relay, and a rule change is not one.

## Goal

When Nubi's remediation plan concludes the alert rule is the cause, the plan itself carries the
fix as an action card with an **Apply** button: the rule before and after, the reason, and a
confidence. No new flow; the plan is still generated on click.

## Decisions (/challenge: REVISE → PROCEED in round 2)

- **D1. The plan gets the facts, not a guess.** On generate, the panel fetches the event's alert
  controls (existing `eventrules_get_event_alert_controls`) and adds a context section
  "## Alert rule this event came from": the rule name, where it is defined and who manages it,
  its current expression and `for`, and exactly which changes are allowed (edit, turn off at
  source, mute scopes with 7-day counts). The response gains `rule_definition {expr, duration}`
  for rules whose edit is available. The panel adds artifact `alert_rule` and sends the allowed
  operations and current definition in the generate request, so the server can check the plan
  without trusting the model.
- **D2. One typed action shape.** An action may carry
  `alert_rule_change: {operation: "edit" | "disable" | "mute", expr?, duration?, mute_scope?}`,
  with an empty `execute_command`. `edit` and `disable` are kind `fix` (the rule is the cause);
  `mute` is kind `mitigation` (it hides the alert, the rule stays wrong).
- **D3. Same bar as the threshold suggestion.** The prompt allows an alert-rule action only when
  Nubi's own hypotheses conclude the rule is wrong and the evidence shows no real fault. For a
  real fault it must not propose touching the rule. The rule is a new bullet next to the
  existing threshold rule in `remediation_generate_json.yaml` (a `prompts/default/v1` change,
  requested explicitly).
- **D4. The server drops what it cannot apply** (`normalizePlan`, deterministic, not prompt
  rules): an operation not in the allowed list; an `edit` whose expression does not parse as
  PromQL, whose `duration` is not a valid duration, or that changes nothing; a `mute` scope not
  offered. Dropped, not repaired.
- **D5. Apply goes through a narrow server operation.** New RPC
  `eventrules_apply_rule_change {rule_id, account_id, expr?, duration?, expected_expr,
  expected_duration}`. The server re-resolves the rule, requires `edit_definition`, re-validates
  the PromQL, refuses when the stored definition no longer matches `expected_*` (the rule changed
  after the plan was made), and otherwise changes only `expr` / `for` on the stored rule through
  the existing `UpdateEventRule` path — so the PrometheusRule patch, Helm warning and locator
  behaviour are the ones already live-tested. The browser never rebuilds the full rule. Turn off
  and mute reuse the existing RPCs and confirm dialogs.

Round 1 (fresh-context adversary) — REVISE, folded in:

- **D7. Artifacts admit actions per kind, not per plan.** Today one artifact name makes every
  command-less action survive (`normalizePlan` collapses `available_artifacts` to one boolean).
  `alert_rule` admits only actions that carry `alert_rule_change`; other command-less actions
  still need `code_fix` / `threshold`. The prompt section says plainly that the rule's definition
  is context, not a prepared fix, and that only `alert_rule_change` may use it.
- **D8. A partial rule update does not wipe the rest of the rule.** `UpdateEventRule` always
  rewrites the agent playbook with the caller's `trigger_params` / `action_params`, so a caller
  that omits them nulls it (threshold apply does this today; 1 of 335 playbooks on dev is already
  null). Fix the class: when both keys are absent (`TriggerParams == nil && ActionParams == nil`),
  `UpdateEventRule` keeps the stored playbook. Empty arrays still clear it — that is how the Alert
  Manager form removes every action. The
  new RPC and threshold apply build their request from one shared "stored rule → EventConfig"
  helper instead of each rehydrating annotations and labels by hand.
- **D9. Validation follows the rule's own dialect.** A proposed edit is validated with the
  Prometheus parser only if the rule's current expression parses with it. A rule whose current
  expression does not (VictoriaMetrics MetricsQL, templating) is not offered for editing, so a
  proposal can never be refused for syntax Nudgebee cannot check.
- **D10. The staleness check compares meaning, not text.** `expected_expr` and the stored
  expression are compared as parsed ASTs (`String()` of both). Two sync passes store the same
  rule with different whitespace, and a raw string compare would refuse valid applies.
- **D11. Nothing is dropped silently.** Every alert-rule action the server drops is logged with
  its reason and kept in the saved plan under `dropped` (not rendered), so "no card" can be told
  apart from "the model did not propose one".

- **D6. Not a remediation record, same as mute.** Applying writes an audit entry, not an
  `event_resolution` row. The card shows "Applied" by reading the rule's current state (the
  controls endpoint), not from a record, using the same AST-normalised compare as D10 so a sync
  that re-serialises the expression does not drop the badge.

## UI

In the "Suggested by Nubi" plan, an alert-rule action renders as a normal action card, with:

- For `edit`: the current and proposed expression / `for`, changed parts highlighted; the
  capability warning (e.g. Helm) under it; button **Apply** → confirm → apply.
- For `disable` / `mute`: one line on what happens; button **Turn off** / **Mute** → the same
  confirm dialogs as "Stop this alert".
- After success the card shows **Applied**; "Stop this alert" refreshes.

## Not in this change

- Cloud expression edits (AWS/GCP/Azure `edit_definition` is unavailable; their thresholds
  already come through the triage threshold artifact).
- Generating the plan automatically.
- Showing Nubi's rule fix outside the plan (e.g. promoted into "Stop this alert").

## Acceptance criteria (dev, local services against dev)

1. `OtelDemoGRPCClientErrorRate` event → Generate Remediation: the plan has a `fix` card
   "Fix alert rule …" whose proposed expression excludes the EventStream method. Apply → the
   `victoria/otel-demo-alerts` PrometheusRule changes only that rule's `expr`; the card shows
   Applied. (Restored from a backup afterwards.)
2. A `PVCPendingMissingStorageClass` event: the plan proposes an `expr` / `for` change for the
   rule in `nudgebee-prometheus.rules`.
3. A real-fault event (the `KubeJobFailed` migration failure): no alert-rule action.
4. Unit tests: invalid PromQL, unchanged expression, operation not allowed, and unknown mute
   scope are dropped; kinds are forced (edit/disable fix, mute mitigation).
5. Apply after the rule was edited elsewhere is refused with "the rule changed since this plan
   was made"; invalid PromQL sent directly to the RPC is refused; a whitespace-only difference in
   the stored expression is not a refusal.
6. Unit tests: with only `alert_rule` available, a command-less action without
   `alert_rule_change` is dropped; `UpdateEventRule` without trigger/action params keeps the
   stored playbook and with empty arrays clears it; a rule whose current expression is not PromQL gets no edit.
7. AC1 check covers the whole PrometheusRule: every other rule and field in the CR is byte-identical
   before and after Apply (backup + diff), and the rule's playbook row is unchanged.

## Verified on dev (2026-09-18/19, local app + api-server + llm-server against dev)

- `OtelDemoGRPCClientErrorRate`: the regenerated plan's fix card "Exclude EventStream from alert
  rule" (95%) showed the rule before/after; Apply changed only that rule's `expr` in
  `victoria/otel-demo-alerts` (whole-CR diff), and the card showed Applied. A stale Apply and an
  invalid expression were refused. Restored through the same path; the CR matches the backup.
- `NGINXTooMany400s` (Helm rule): a fix card replacing the cumulative-counter ratio with a
  5-minute `rate()` ratio, excluding the catch-all host and requiring at least 1 req/s. Not
  applied (chart-owned).
- `dd-log-demo` (Datadog synthetic test): a mute card, scope every workload — the only change the
  rule allowed.
- Real faults (`KubeJobFailed` migration failure, `ApplicationAPIFailures` auth failure,
  `report_crash_loop` missing env) and an undetermined case (`HighP95Latency`): no alert-rule
  action. No proposal was dropped by the server checks in these runs.
- Not clicked live: Apply on a plan-proposed mute or turn-off (both reuse the live-tested
  "Stop this alert" paths).
