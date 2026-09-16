# Spec — Real alert-rule edit / disable for AWS, GCP, Azure and in-cluster Prometheus

Epic: #38553 (phase 1). Baseline: `origin/main` @ `32a999003e`.
Revision 3.2 (2026-09-16) — PR 1 findings folded in (see "Rev 3.2 corrections"). Revision 3 — after two `/challenge` REVISE verdicts. Rev 2: mute vs disable-at-source split, no JSONB ambiguity flag, one availability function, customer CRs in scope. Rev 3/3.1: mute is a triage suppression rule (anchored, escaped alertname; one rule per source) (no new column, no ingestion filters); `enabled` ownership defined; GitOps classification via `managedFields`; agent delete payload made safe; rollout gaps closed.

Out of scope: Datadog, New Relic, Dynatrace, Splunk, Grafana, Elasticsearch, Chronosphere, SigNoz, CubeAPM, Loki, agentless Prometheus (`prometheus_user`, `prometheus_alertmanager_webhook`), webhook-only sources. For those, phase 1 only (a) offers *mute* and (b) stops edit/disable from returning fake success.

## Goal

When a user (Alert Manager, Alert Tuning) — later Nubi — edits the threshold / evaluation window of, mutes, or disables an alert rule from AWS CloudWatch, GCP Monitoring, Azure Monitor or an in-cluster PrometheusRule, the change takes effect where the user expects, keeps everything not intended to change, can be undone, and fails visibly when it can't be applied. Controls that cannot work are not shown.

## Rows in scope (dev)

| `event_rules.source` | rows | locator today |
|---|---|---|
| `prometheus`, CR-based (`is_editable=true`) | 1635 | `namespace`+`name` on 346 rows; group in `category` |
| `prometheus`, rules-API based (`is_editable=false`) | 1093 | none → read-only (mute only) |
| `AWS_CloudWatch_Alarm` | 551 | `labels.aws_region` + `labels.aws_alarm` |
| `GCP_Metric_Alert` | 365 | `labels.gcp_policy_id` |
| `Azure_Monitor_Alert` | 67 | none (collector computes ARM id, drops it); some rows are `scheduled_query_rule` |

## Decisions

### D1. Two different actions: *Mute in Nudgebee* and *Disable at source*
- **Mute** = an existing **triage suppression rule** (`RuleTypeSuppression` / `ActionSuppress`, `api-server/services/triage/rules.go`, evaluated by `CheckTriageRules` for every event regardless of which service inserted it) created from Alert Manager with `match_alertname` = rule alert, `account_id` = rule account, and `match_source` = the rule's event-source set (explicit map: `AWS_CloudWatch_Alarm` → {`AWS_CloudWatch_Alarm`, `AWS_EventBridge`}; `prometheus` → {`prometheus`}; webhook twins → themselves). No new column, no ingestion-time drop: events are still stored and resolve normally, they get `nb_status=SUPPRESSED`. Unmute = disable that triage rule. Alert Manager shows "Muted" when an enabled suppression rule matches the row; Triage's rule list shows the same rule — one concept.
  - **Encoding**: `match_alertname` is an unanchored regex (`triage/rules.go` `matchesRegex`, compile errors never match). The mute rule stores `"^" + regexp.QuoteMeta(alert) + "$"`; Alert Manager's "is muted" check builds the identical string. Unit test with an AWS alarm name like `API (5xx) > 1% [prod]`.
  - **Source set**: `match_source` is exact single-string equality, so mute creates **one suppression rule per source** in the set (AWS → 2 rules), all with `action_value = "mute:<event_rules.id>"`. Mute/unmute creates/disables the whole group in one transaction; "Muted" = `EXISTS (enabled rule WHERE action_value = 'mute:<id>')`. No schema change.
  - No `match_occurrence_greater_than`, so the first occurrence is suppressed too. No expiry by default (`effective_until` null).
  - **Visibility (decided)**: suppressed events already send no notification (`notification/event_processor.go`) and skip LLM analysis (`llm/event_processor.go`). The default events views are **not** changed (that would alter every existing suppression rule's behaviour); instead muted/suppressed events show a "Muted" badge.
- **Disable at source** = the upstream rule stops evaluating. Only where it has no side effect beyond that rule:
  - GCP: policy `enabled=false` (update mask `enabled`).
  - Azure metric alert: `enabled=false` (Get → CreateOrUpdate). `scheduled_query_rule` / activity-log rows: unsupported.
  - Prometheus: remove the rule from its PrometheusRule; re-enable re-adds the stored definition.
  - **AWS: not offered.** CloudWatch has no alarm-level disable; `DisableAlarmActions` would silence the customer's own paging. AWS gets mute + edit only.
- **`enabled` ownership**: `enabled` = "the rule is live upstream". Syncers write it on insert **and** on conflict (`enabled = EXCLUDED.enabled` added to both `alert_rules_handler.py` and `StoreEventRules` SET lists; GCP/Azure from the provider flag). api-server writes `enabled=false` after a successful Prometheus disable-at-source (the syncer never sees an absent rule) and `true` after re-enable; if the customer re-adds the rule, the next sync sets it back to `true`. GCP stops skipping disabled policies in both places (`gcloud_monitoring_alerts_common.go:56-58` and `:581`).

### D2. One availability + routing function
`eventrule.ResolveRuleTarget(ctx, ruleID)` — tenant- and account-scoped lookup by `id` — returns the target (`aws|gcp|azure|prom_cr|unsupported`), its locator, and per-operation availability with a reason: `{update_threshold, update_duration, disable_at_source, mute, pr}`.
- Used by `UpdateEventRule`, `DisableEventRule`, a new RPC `eventrule_get_capabilities`, **and** triage `GetThresholdApplyOptions` / `applyDirect` (so Alert Tuning and Alert Manager never disagree).
- Replaces `isExternalProviderSource` / `cloudAlertProvider` for the in-scope sources (single source-name map).
- Provider write first; DB write only on success; provider errors returned. Out-of-scope sources: edit/disable return `unsupported` (mute still works).
- Triage keeps using the event's `alert_rule_key` (AWS alarm ARN) as the AWS locator — more precise than the row. `LoadSourceRule` filters by source.

### D3. Rule identity: dedicated syncer-owned columns
Migration adds `event_rules.ambiguous boolean default false` and `event_rules.origin text` (`provider` | `nudgebee_ui` | `nudgebee_cr` | `chart_cr` | `customer_cr`).
- Syncers set `ambiguous=true` when they collapse more than one upstream rule into one `(account, tenant, source, alert)` row (AWS same name in two regions; same alert in two PrometheusRules/groups; GCP duplicate display names; Azure same name across resource groups). Ambiguous rows: edit / disable-at-source unavailable in Alert Manager; mute still available; triage apply still works for AWS via the event ARN.
- Syncers write `external_rule_id` (AWS ARN, GCP policy name, Azure ARM id) and upstream `enabled`.
- Every writer uses explicit column lists: `upsertEventRule` never touches `ambiguous`/`origin`; nobody replaces `provider_config` wholesale except the owner of the keys it writes (merge with `||`).
- Readers of the new columns tolerate their absence (fail open, log) so cloud-collector / k8s-collector can deploy before or after the migration job.
- Changing the unique key stays deferred.

### D4. Cloud edits reuse the existing route
`/v1/cloud/update_alert_rule` (cloud-collector) is extended, not duplicated: honour the already-present `Enabled` field for GCP/Azure; threshold becomes optional. Fixes inside the existing updaters:
- AWS `DatapointsToAlarm` scales proportionally with `EvaluationPeriods` (3-of-3 → 10-of-10), never 3-of-10.
- GCP/Azure: threshold edit only when every condition/criterion shares one threshold (else unavailable with reason). Azure lookup by ARM id, not name.
- Revert state: `event_threshold_suggestions` gains `previous_provider_config jsonb` (previous `EvaluationPeriods`, `DatapointsToAlarm`, per-condition thresholds) so revert restores exactly.

### D5. Prometheus: customer PrometheusRules included
- **Agent payload**: extend the existing `create_or_replace_alert_rule` / `delete_alert_rule` legacy payload with optional `namespace`, `name`, `group`. When present, the agent edits that rule in that CR: patches only `expr` / `for` (labels, annotations, other rules untouched); delete removes only that rule. The locator shape **requires a non-empty `alert`** and never deletes a whole CR — today's `handleDeletePromRule` falls through to `DeletePrometheusRule(namespace, name)` when `alert` is empty (`runner/pkg/mutate/handlers.go`), so whole-CR delete moves behind an explicit `delete_cr: true`. api-server sends `name` only when the agent reports a version that has this change. Without a locator, today's `nudgebee-prometheus.rules` behaviour is unchanged. No new action name.
- **RBAC**: chart gains an opt-in value (e.g. `alertRules.clusterWideWrite`, default `false`) that adds a ClusterRole for `prometheusrules` `get,update,patch`; today's namespaced Role remains for the install namespace. Without it, customer-CR operations report `unavailable: agent lacks permission — enable alertRules.clusterWideWrite`.
- **Agent capability reporting**: reuse the existing channel — relay greeting stores `agent.version` (`relay-server/pkg/db/db_store.go`), agent telemetry (`runner/pkg/telemetry/service.go`) already sends `LightActions` (→ `mutate_enabled`). Add `prometheusrule_cluster_write` to telemetry; k8s-collector `telemetry_handler.py` persists it into `connection_status`. `ResolveRuleTarget` reads these; missing → `unavailable: agent too old`.
- **Ownership / GitOps** (captured by k8s-collector into `origin` + `provider_config.managed_by`), decided from `metadata.managedFields[].manager` first, labels/annotations second:
  - `nudgebee-prometheus.rules` in the install namespace → `nudgebee_cr`: direct edit/disable.
  - The agent chart's own rules CR (name `<release>.rules` with `app.kubernetes.io/name` = the agent chart name, in the install namespace) → `chart_cr`: direct edit with warning that a chart upgrade restores defaults.
  - Manager `argocd-controller`, Argo label tracking (`app.kubernetes.io/instance` matching an Argo app) or annotation tracking (`argocd.argoproj.io/*`), or manager `kustomize-controller` / `helm-controller` (Flux) → `customer_cr`, direct write **unavailable**; PR only.
  - Manager `helm` / `meta.helm.sh/*` (plain Helm, e.g. kube-prometheus-stack) → direct write **with warning** that the next `helm upgrade` reverts it; PR offered alongside.
  - Only manager `kubectl*` / no managers → unmanaged → direct write.
  - Precedence: `nudgebee_cr` > `chart_cr` > Argo/Flux > Helm > unmanaged. Metadata unreadable or ambiguous → PR only.
- k8s-collector persists `group` (not only `category`), `origin`, `managed_by`, sets `ambiguous`, and stops dropping `namespace`/`name` on existing rows.
- `relay.Execute` treats `data.success=false` as failure.

### D6. Permissions
No CloudFormation change (AWS disable-at-source is not offered; threshold edit already uses `PutMetricAlarm`). GCP write needs `monitoring.alertPolicies.update`: `ResolveRuleTarget` reports it from the existing credential validator.

## Acceptance criteria (dev evidence in each PR)

- **AC-1 Mute (all sources).** Mute an AWS alarm on an EventBridge-enabled account and a Prometheus alert → force the condition (`aws cloudwatch set-alarm-state`, a firing test rule) → new events on both the poll and EventBridge paths are `SUPPRESSED` with a Muted badge and no notification is sent; muting an alarm whose name contains regex metacharacters works and does not mute a longer same-prefix alarm; an event that was already open when muting still resolves; the customer's alarm/rule is unchanged (`describe-alarms`, `kubectl get prometheusrule -o yaml` diff empty). The same rule appears in Triage's rule list. Unmute → next firing is a normal event.
- **AC-2 AWS edit.** Threshold and window edits land on the alarm; `DatapointsToAlarm` scaled proportionally; actions/dimensions unchanged; revert restores exact previous `Threshold`/`EvaluationPeriods`/`DatapointsToAlarm`. No "Disable at source" control is shown for AWS.
- **AC-3 GCP.** Disable/enable flips `enabled` (`gcloud alpha monitoring policies describe`), channels/docs unchanged; single-condition threshold edit works; mixed-threshold policy shows the edit unavailable with a reason.
- **AC-4 Azure.** Disable/enable and threshold edit hit the alert with the stored ARM id even when a same-named alert exists in another resource group; a `scheduled_query_rule` row shows edit/disable unavailable.
- **AC-5 Prometheus.** With `clusterWideWrite=true`: edit + disable + enable on a rule in (a) `nudgebee-prometheus.rules`, (b) an unmanaged customer CR, (c) a kube-prometheus-stack CR (warning shown) change only that rule; no duplicate appears in `nudgebee-prometheus.rules`. (d) an Argo-tracked CR shows PR only. With `clusterWideWrite=false`, (b)/(c) show the permission reason. An agent without the new status fields shows "agent too old".
- **AC-6 Fail visibly.** Provider/agent error (permission removed, `success:false`) → UI error; `event_rules` row and suggestion status unchanged.
- **AC-7 No fake success.** `datadog_webhook`, `chronosphere`, `zenduty_webhook`, rules-API `prometheus` rows: edit/disable controls hidden; direct API calls return `unsupported`; mute works.
- **AC-8 Sync fidelity.** After a collector run: cloud rows carry `external_rule_id`; a policy/alert disabled in the cloud console shows disabled and re-enabling it there shows enabled; a same-name AWS alarm pair produces `ambiguous=true`; a UI edit does not clear `ambiguous`/`origin`; a Prometheus rule disabled from Nudgebee stays disabled across syncs and flips to enabled if re-added to the CR by hand.
- **AC-11 Agent safety.** A `delete_alert_rule` request with a locator and empty `alert` is rejected by the agent (unit test); whole-CR delete only with `delete_cr: true`.
- **AC-9 One answer.** For the same rule, Alert Tuning apply options and `eventrule_get_capabilities` agree (test).
- **AC-10 Tenant safety.** Rule id from another tenant → 403/404 before any provider/relay call (test).

## Work breakdown (order is binding; nothing user-visible breaks between PRs)

1. **Migration + resolver + mute + capabilities + app, together** (AC-1, AC-6, AC-7, AC-9, AC-10): `ambiguous`/`origin` columns and `previous_provider_config`; `ResolveRuleTarget`; Mute via triage suppression rules (escaped anchored alertname, one rule per source with a shared `mute:<id>` marker, Muted badge); `eventrule_get_capabilities`; `GetThresholdApplyOptions` delegates; `LoadSourceRule` source filter; tenant scoping; `relay.Execute` success check; app Alert Manager/Alert Tuning read capabilities and add Mute. Out-of-scope edit/disable become `unsupported` in the same release as the UI hides them. Migration applied before the api-server rollout (migration job runs first); all readers fail open.
2. **cloud-collector sync + update fixes** (AC-2, AC-3, AC-4, AC-8 cloud part): external id, `enabled` on conflict, both GCP skips removed, Azure ARM id + alert type, ambiguity, `DatapointsToAlarm`, single-threshold guard, `Enabled` honoured for GCP/Azure; api-server wires them into the resolver.
3. **k8s-agent + chart** (agent repo, AC-11): locator-aware legacy payload with non-empty `alert`, `delete_cr` guard, rule-level patch, opt-in cluster-wide RBAC, `prometheusrule_cluster_write` in telemetry. Not verifiable end-to-end until PR 4 lands.
4. **k8s-collector + api-server Prometheus wiring** (AC-5, AC-8 Prometheus part): `group`/`origin`/`managed_by` (from `managedFields`)/`ambiguous`, `enabled` on conflict; `telemetry_handler.py` persists agent capability fields; resolver Prometheus branch gated on agent version/capabilities.

## Nubi (later phase, decided)
Nubi may recommend **both** *Mute in Nudgebee* and *Disable at source* (plus threshold/window edits), but only actions `eventrule_get_capabilities` reports as available for that rule, and only when its hypotheses conclude the alert itself is wrong. It proposes; a user applies. Disable at source is never offered for AWS (not available per D1).

## Rev 3.2 corrections (found while building PR 1)

- **Mute marker is JSON.** `action_value` is `jsonb` and validated as JSON, so the marker is `{"mute_event_rule_id": "<event_rules.id>"}`, not the string `mute:<id>`.
- **Mute is offered only where events carry the rule name.** `match_alertname` matches `events.aggregation_key`. On dev (14 days) that equals `event_rules.alert` for 100% of AWS (poll and EventBridge), GCP and Prometheus events, but only 44% of `datadog_webhook` and 60% of `zenduty_webhook` events (their keys are monitor / incident ids). Mute is therefore offered for `AWS_CloudWatch_Alarm`, `AWS_EventBridge`, `GCP_Metric_Alert`, `Azure_Monitor_Alert` (poll path) and `prometheus` / legacy `nudgebee`; other sources show it unavailable with a pointer to Triage suppression rules. AC-1/AC-7 read accordingly.
- **Mute takes up to 60 s on other replicas** (the per-pod triage rule cache TTL); the replica that handled the request clears its cache at once.
- **Alert Manager's edit form is its own operation** (`edit_definition`): the form writes expr/for/annotations, which only the Prometheus paths (agent CR, own ruler) and Nudgebee-created external rules accept. Cloud threshold edits stay in Alert Tuning until PR 2.
- **Rules Nudgebee created in a SaaS provider** (`datadog`, `newrelic`, … sources — none on dev) keep their provider route in PR 1, but provider errors are now returned instead of swallowed. Marking them unsupported would have removed a path that works for some providers; fixing each provider stays in epic workstream 5.
- **Legacy `nudgebee` source rows** (old UI-created PrometheusRule entries) route like `prometheus`. Updates now go by row id and keep the stored source, which also stops the duplicate `prometheus` row an update used to create.
- **Azure is still found by name in PR 1.** Suggestions key Azure by alert name, and cloud-collector already searches every subscription by name; the resolver keeps that path with a warning until PR 2 stores the ARM id. `azure_monitor_webhook` rows resolve to Azure, as triage apply always routed them.
- **Agent `success:false` check** is a helper (`relay.ActionSucceeded`) called by the alert-rule writes only. `relay.Execute` has 66 callers and is not changed in this PR.
- **Updates keep unshown fields.** For a known row, the edit form's severity and three standard annotations are laid over the stored labels and annotations, both upstream and in `event_rules`.

## Open questions
- Q1. Helm-managed customer CRs: is "direct write with warning" acceptable, or PR-only?
