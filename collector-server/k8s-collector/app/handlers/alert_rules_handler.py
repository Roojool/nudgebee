import logging
from typing import Any, Dict, List, Optional, Tuple

import psycopg2

from db import database

# Must name exactly the columns of event_rules_account_tenant_source_alert_key
# (migration V873). Postgres infers the arbiter index by exact column match, so a
# stale target raises 42P10 "no unique or exclusion constraint matching the ON
# CONFLICT specification" at plan time — before any conflict exists — which fails
# the whole batch and silently stops rule ingestion for every account.
EVENT_RULE_CONFLICT_COLUMNS = ("account_id", "tenant_id", "source", "alert")

_CONFLICT_TARGET = f"ON CONFLICT({','.join(EVENT_RULE_CONFLICT_COLUMNS)}) DO UPDATE SET "

event_rule_on_conflict = (
    _CONFLICT_TARGET + "alert=EXCLUDED.alert, "
    "expr=EXCLUDED.expr, annotations=EXCLUDED.annotations, duration=EXCLUDED.duration, "
    "severity=EXCLUDED.severity, source=EXCLUDED.source, category=EXCLUDED.category, "
    "namespace=EXCLUDED.namespace, name=EXCLUDED.name"
)

# Rules read from the Prometheus rules API carry no PrometheusRule, so this pass
# must not touch the columns the CRD pass owns (namespace, name, group, is_editable,
# origin, ambiguous, provider_config): its rows lack them, and EXCLUDED would reset
# them to NULL/defaults whenever the CRD listing is missing from a payload.
# Nor `enabled`: Prometheus keeps listing a rule api-server just removed from its
# PrometheusRule until it reloads, so this pass would switch a disabled rule back on.
api_rule_on_conflict = (
    _CONFLICT_TARGET + "alert=EXCLUDED.alert, "
    "expr=EXCLUDED.expr, annotations=EXCLUDED.annotations, duration=EXCLUDED.duration, "
    "severity=EXCLUDED.severity, source=EXCLUDED.source, category=EXCLUDED.category"
)

# The CRD pass owns where a rule is defined and who manages it. `enabled` mirrors
# presence: a rule api-server removed (disable) is absent from the listing and keeps
# enabled=false; a rule someone re-added reads as enabled again. provider_config is
# merged so keys other writers own survive.
_crd_rule_on_conflict_legacy = (
    event_rule_on_conflict + ', "group"=EXCLUDED."group", enabled=EXCLUDED.enabled, is_editable=EXCLUDED.is_editable, '
    "provider_config=COALESCE(event_rules.provider_config, '{}'::jsonb) || EXCLUDED.provider_config"
)
crd_rule_on_conflict = _crd_rule_on_conflict_legacy + ", origin=EXCLUDED.origin, ambiguous=EXCLUDED.ambiguous"

# The PrometheusRule the agent writes UI-created rules into.
NUDGEBEE_RULES_CR = "nudgebee-prometheus.rules"
AGENT_CHART_NAME = "nudgebee-agent"

# Who manages a PrometheusRule, and so how api-server may change its rules.
ORIGIN_NUDGEBEE_CR = "nudgebee_cr"
ORIGIN_CHART_CR = "chart_cr"
ORIGIN_CUSTOMER_CR = "customer_cr"

MANAGED_BY_NUDGEBEE = "nudgebee"
MANAGED_BY_CHART = "chart"
MANAGED_BY_GITOPS = "gitops"
MANAGED_BY_HELM = "helm"
MANAGED_BY_UNMANAGED = "unmanaged"
MANAGED_BY_UNKNOWN = "unknown"

_GITOPS_MANAGERS = ("argocd", "kustomize-controller", "helm-controller")
_GITOPS_KEY_PREFIXES = ("argocd.argoproj.io/", "kustomize.toolkit.fluxcd.io/", "helm.toolkit.fluxcd.io/")


def classify_prometheus_rule(metadata: Dict[str, Any]) -> Tuple[str, str]:
    """Return (origin, managed_by) for a PrometheusRule's metadata.

    Precedence: Nudgebee's own CR > the agent chart's CR > Argo CD / Flux >
    plain Helm > unmanaged. managedFields (what really wrote the object) is read
    first, labels/annotations second. With no managers and no ownership marks the
    manager is unknown, and api-server offers a PR only."""
    name = metadata.get("name") or ""
    labels = _dict(metadata.get("labels"))
    annotations = _dict(metadata.get("annotations"))
    managers = [str(f.get("manager") or "") for f in _dicts(metadata.get("managedFields"))]

    if name == NUDGEBEE_RULES_CR:
        return ORIGIN_NUDGEBEE_CR, MANAGED_BY_NUDGEBEE
    if labels.get("app.kubernetes.io/name") == AGENT_CHART_NAME:
        return ORIGIN_CHART_CR, MANAGED_BY_CHART

    marks = list(labels.keys()) + list(annotations.keys())
    if any(m.startswith(_GITOPS_MANAGERS) for m in managers) or any(k.startswith(_GITOPS_KEY_PREFIXES) for k in marks):
        return ORIGIN_CUSTOMER_CR, MANAGED_BY_GITOPS
    if (
        "helm" in managers
        or labels.get("app.kubernetes.io/managed-by") == "Helm"
        or "meta.helm.sh/release-name" in annotations
    ):
        return ORIGIN_CUSTOMER_CR, MANAGED_BY_HELM
    if managers:
        return ORIGIN_CUSTOMER_CR, MANAGED_BY_UNMANAGED
    return ORIGIN_CUSTOMER_CR, MANAGED_BY_UNKNOWN


def _dedup_rules(rules: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Deduplicate by unique-constraint keys so a single batch INSERT with
    ON CONFLICT DO UPDATE doesn't hit PostgreSQL's 'cannot affect row a
    second time' error. Last-seen rule wins, matching prior sequential behavior.

    Keyed on the same columns as EVENT_RULE_CONFLICT_COLUMNS — a narrower key
    would collapse rows the arbiter treats as distinct."""
    seen: Dict[Tuple[str, ...], Dict[str, Any]] = {}
    for r in rules:
        seen[tuple(r[c] for c in EVENT_RULE_CONFLICT_COLUMNS)] = r
    return list(seen.values())


def _collapse_crd_rules(rules: List[Dict[str, Any]], install_namespace: Optional[str]) -> List[Dict[str, Any]]:
    """One row per unique key. When the same alert is defined in several places
    the row is ambiguous — api-server then refuses to write to an arbitrary copy —
    unless exactly one copy lives in this agent's install namespace (clusters with
    several agents each carry a nudgebee-prometheus.rules)."""
    by_key: Dict[Tuple[str, ...], List[Dict[str, Any]]] = {}
    for r in rules:
        by_key.setdefault(tuple(r[c] for c in EVENT_RULE_CONFLICT_COLUMNS), []).append(r)

    out = []
    for copies in by_key.values():
        if len(copies) == 1:
            out.append(copies[0])
            continue
        local = [c for c in copies if install_namespace and c.get("namespace") == install_namespace]
        if len(local) == 1:
            out.append(local[0])
            continue
        chosen = dict(copies[-1])
        chosen["ambiguous"] = True
        out.append(chosen)
    return out


def _install_namespace(cloud_account_id: str) -> Optional[str]:
    try:
        rows = database.run_query(
            "SELECT connection_status->>'installationNamespace' FROM agent "
            "WHERE cloud_account_id = %s AND type = 'k8s' AND connection_status->>'installationNamespace' IS NOT NULL "
            "ORDER BY last_connected_at DESC NULLS LAST LIMIT 1",
            [cloud_account_id],
        )
    except Exception:
        logging.warning(f"alert_rules: cannot read install namespace for {cloud_account_id}", exc_info=True)
        return None
    return rows[0][0] if rows else None


def _dicts(value: Any) -> List[Dict[str, Any]]:
    """The dict entries of a list that may be null or hold junk (agent payloads
    are raw Kubernetes / Prometheus JSON; any field can be null)."""
    return [v for v in value if isinstance(v, dict)] if isinstance(value, list) else []


def _dict(value: Any) -> Dict[str, Any]:
    return value if isinstance(value, dict) else {}


def _rule_severity(labels: Dict[str, Any]) -> str:
    severity = labels.get("severity")
    severity = severity.lower() if isinstance(severity, str) else "warning"
    return severity if severity in ("critical", "warning") else "warning"


def handle_api_based_rules(rules: Dict[str, Any], tenant: str, cloud_account_id: str):
    alert_rules_list = []
    for group in _dicts(rules.get("groups")):
        group_name = group.get("name")
        for rule in _dicts(group.get("rules")):
            if rule.get("type") != "alerting" or not rule.get("name"):
                continue
            labels = _dict(rule.get("labels"))
            alert_rule = {
                "tenant_id": tenant,
                "account_id": cloud_account_id,
                "expr": rule.get("query"),
                "annotations": _dict(rule.get("annotations")),
                "duration": rule.get("duration"),
                "labels": labels,
                "alert": rule.get("name"),
                "severity": _rule_severity(labels),
                "source": "prometheus",
                "category": group_name,
                "enabled": True,
                "is_editable": False,
            }
            alert_rules_list.append(alert_rule)
    if alert_rules_list:
        alert_rules_list = _dedup_rules(alert_rules_list)
        database.insert_data("event_rules", alert_rules_list, on_conflict=api_rule_on_conflict)
    return alert_rules_list


def build_crd_rules(
    rules: Dict[str, Any], tenant: str, cloud_account_id: str, install_namespace: Optional[str]
) -> List[Dict[str, Any]]:
    alert_rules_list = []
    for item in _dicts(rules.get("items")):
        metadata = _dict(item.get("metadata"))
        namespace = metadata.get("namespace")
        prometheus_rule_name = metadata.get("name")
        origin, managed_by = classify_prometheus_rule(metadata)
        for group in _dicts(_dict(item.get("spec")).get("groups")):
            for rule in _dicts(group.get("rules")):
                if not rule.get("alert"):
                    continue
                labels = _dict(rule.get("labels"))
                alert_rule = {
                    "tenant_id": tenant,
                    "account_id": cloud_account_id,
                    "expr": rule.get("expr"),
                    "annotations": _dict(rule.get("annotations")),
                    "duration": rule.get("for"),
                    "labels": labels,
                    "alert": rule.get("alert"),
                    "severity": _rule_severity(labels),
                    "source": "prometheus",
                    "category": group.get("name"),
                    '"group"': group.get("name"),
                    "enabled": True,
                    "is_editable": True,
                    "namespace": namespace,
                    "name": prometheus_rule_name,
                    "provider_config": {"managed_by": managed_by},
                    "origin": origin,
                    "ambiguous": False,
                }
                alert_rules_list.append(alert_rule)
    return _collapse_crd_rules(alert_rules_list, install_namespace)


def handle_crd_based_rules(rules: Dict[str, Any], tenant: str, cloud_account_id: str):
    alert_rules_list = build_crd_rules(rules, tenant, cloud_account_id, _install_namespace(cloud_account_id))
    if not alert_rules_list:
        return alert_rules_list
    try:
        database.insert_data("event_rules", alert_rules_list, on_conflict=crd_rule_on_conflict)
    except psycopg2.errors.UndefinedColumn:
        # origin/ambiguous arrive with migration V941; write the rest until it runs.
        logging.warning("alert_rules: event_rules.origin/ambiguous missing; storing without them")
        legacy = [{k: v for k, v in r.items() if k not in ("origin", "ambiguous")} for r in alert_rules_list]
        database.insert_data("event_rules", legacy, on_conflict=_crd_rule_on_conflict_legacy)
    return alert_rules_list


def handle_alert_rules(rules: Dict[str, Any], tenant: str, cloud_account_id: str):
    if rules.get("api_based_rules"):
        handle_api_based_rules(rules.get("api_based_rules"), tenant, cloud_account_id)
    if rules.get("crd_based_rules"):
        handle_crd_based_rules(rules.get("crd_based_rules"), tenant, cloud_account_id)
    if rules.get("chronosphere_alert_rules"):
        handle_chrono_monitors(rules.get("chronosphere_alert_rules"), tenant, cloud_account_id)
    else:
        handle_crd_based_rules(rules, tenant, cloud_account_id)


def handle_chrono_monitors(monitors: Dict[str, Any], tenant: str, cloud_account_id: str):
    alert_rules = []
    for rule in monitors.get("monitors", []):
        if not rule.get("slug"):
            continue
        series_conditions = rule.get("series_conditions", {}).get("defaults", {})
        severity = next(iter(series_conditions), None)

        if severity not in ("critical", "warning"):
            severity = "warning"

        annotations = rule.get("annotations", {})
        if "Summary" in annotations:
            annotations["description"] = annotations["Summary"]
        elif "Message" in annotations:
            annotations["description"] = annotations["Message"]
        annotations["summary"] = rule.get("name", "")
        alert_rule = {
            "tenant_id": tenant,
            "account_id": cloud_account_id,
            "expr": rule.get("prometheus_query"),
            "annotations": annotations,
            "duration": rule.get("interval_secs"),
            "labels": rule.get("labels", {}),
            "alert": rule.get("slug"),
            "severity": severity,
            "source": "chronosphere",
            "category": rule.get("collection_slug", ""),
            "enabled": True,
            "is_editable": False,
            "namespace": "",
            "name": rule.get("slug"),
        }
        alert_rules.append(alert_rule)
    if alert_rules:
        alert_rules = _dedup_rules(alert_rules)
        database.insert_data("event_rules", alert_rules, on_conflict=event_rule_on_conflict)
    return alert_rules
