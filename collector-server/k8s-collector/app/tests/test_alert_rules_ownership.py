"""Prometheus rule sync: ownership, ambiguity and column ownership.

api-server decides from these columns whether a Prometheus rule may be changed
directly, with a warning, or only through a PR, and where the change goes. Runs
offline: psycopg2 and the DB are stubbed.
"""

import os
import sys
import unittest
from unittest import mock

for _k, _v in {
    "ENV": "DEV",
    "COLLECTOR_MODE": "worker",
    "COLLECTOR_DB_URL": "postgresql://u:p@localhost:5432/db?sslmode=disable",
    "NUDGEBEE_ENCRYPTION_KEY": "test-key",
    "ACTION_API_SERVER_TOKEN": "",
    "SERVICE_API_SERVER_URL": "http://localhost:8000",
    "RABBIT_MQ_HOST": "localhost",
    "RABBIT_MQ_PORT": "5672",
    "RABBIT_MQ_USERNAME": "guest",
    "RABBIT_MQ_PASSWORD": "guest",
    "REDIS_SERVER_HOST": "localhost",
    "REDIS_SERVER_PORT": "6379",
    "REDIS_USER_NAME": "",
    "REDIS_USER_PASSWORD": "",
    "CLICKHOUSE_ENABLED": "false",
    "CLICKHOUSE_HOST": "",
    "CLICKHOUSE_USER": "",
    "CLICKHOUSE_PASSWORD": "",
}.items():
    os.environ.setdefault(_k, _v)


class _UndefinedColumn(Exception):
    pass


for _mod in ("redis", "psycopg2", "psycopg2.extras", "psycopg2.pool", "psycopg2.sql", "clickhouse_driver"):
    sys.modules.setdefault(_mod, mock.MagicMock())

_APP_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if _APP_DIR not in sys.path:
    sys.path.insert(0, _APP_DIR)

from handlers import alert_rules_handler as h  # noqa: E402


def _meta(name, managers=(), labels=None, annotations=None, namespace="monitoring"):
    return {
        "name": name,
        "namespace": namespace,
        "labels": labels or {},
        "annotations": annotations or {},
        "managedFields": [{"manager": m} for m in managers],
    }


class ClassifyTest(unittest.TestCase):
    def test_precedence_and_managers(self):
        cases = [
            # Nudgebee's own CR wins over anything that touched it.
            (_meta("nudgebee-prometheus.rules", ["kubectl-replace", "argocd-controller"]), ("nudgebee_cr", "nudgebee")),
            # The agent chart's own rules: helm-managed but ours.
            (
                _meta("nb.rules", ["helm"], labels={"app.kubernetes.io/name": "nudgebee-agent"}),
                ("chart_cr", "chart"),
            ),
            (_meta("x", ["argocd-application-controller"]), ("customer_cr", "gitops")),
            (_meta("x", ["kustomize-controller"]), ("customer_cr", "gitops")),
            (_meta("x", ["helm-controller"]), ("customer_cr", "gitops")),
            # Argo tracking by annotation, even when helm wrote the object.
            (
                _meta("x", ["helm"], annotations={"argocd.argoproj.io/tracking-id": "app:/x"}),
                ("customer_cr", "gitops"),
            ),
            (_meta("x", ["helm"]), ("customer_cr", "helm")),
            (
                _meta("x", ["OpenAPI-Generator"], labels={"app.kubernetes.io/managed-by": "Helm"}),
                ("customer_cr", "helm"),
            ),
            (_meta("x", [], annotations={"meta.helm.sh/release-name": "kps"}), ("customer_cr", "helm")),
            (_meta("x", ["kubectl-client-side-apply", "kubectl-annotate"]), ("customer_cr", "unmanaged")),
            (_meta("x", []), ("customer_cr", "unknown")),
        ]
        for meta, want in cases:
            with self.subTest(meta=meta):
                self.assertEqual(h.classify_prometheus_rule(meta), want)


def _cr(name, namespace, groups, managers=("kubectl-client-side-apply",), labels=None):
    return {
        "metadata": _meta(name, managers, labels=labels, namespace=namespace),
        "spec": {"groups": [{"name": g, "rules": rules} for g, rules in groups.items()]},
    }


class BuildRowsTest(unittest.TestCase):
    def test_rows_carry_location_and_ownership(self):
        payload = {
            "items": [
                _cr(
                    "payments", "monitoring", {"pay": [{"alert": "HighErrors", "expr": "x > 1", "for": "5m"}]}, ["helm"]
                ),
                _cr("nudgebee-prometheus.rules", "nb", {"kubernetes-apps": [{"alert": "Mine", "expr": "up == 0"}]}),
            ]
        }
        rows = {r["alert"]: r for r in h.build_crd_rules(payload, "t", "a", "nb")}
        pay = rows["HighErrors"]
        self.assertEqual((pay["namespace"], pay["name"], pay['"group"']), ("monitoring", "payments", "pay"))
        self.assertEqual((pay["origin"], pay["provider_config"]), ("customer_cr", {"managed_by": "helm"}))
        self.assertTrue(pay["enabled"])
        self.assertTrue(pay["is_editable"])
        self.assertFalse(pay["ambiguous"])
        self.assertEqual(rows["Mine"]["origin"], "nudgebee_cr")

    def test_same_alert_in_two_places_is_ambiguous(self):
        payload = {
            "items": [
                _cr("a", "team-a", {"g": [{"alert": "Dup", "expr": "1"}]}),
                _cr("b", "team-b", {"g": [{"alert": "Dup", "expr": "2"}]}),
            ]
        }
        rows = h.build_crd_rules(payload, "t", "a", "nb")
        self.assertEqual(len(rows), 1)
        self.assertTrue(rows[0]["ambiguous"])

    def test_same_alert_in_two_groups_of_one_cr_is_ambiguous(self):
        payload = {
            "items": [_cr("a", "ns", {"g1": [{"alert": "Dup", "expr": "1"}], "g2": [{"alert": "Dup", "expr": "2"}]})]
        }
        self.assertTrue(h.build_crd_rules(payload, "t", "a", None)[0]["ambiguous"])

    def test_copy_in_install_namespace_wins(self):
        # A cluster with several agents: each namespace has its own Nudgebee CR.
        payload = {
            "items": [
                _cr("nudgebee-prometheus.rules", "agent-prod", {"k": [{"alert": "Dup", "expr": "prod"}]}),
                _cr("nudgebee-prometheus.rules", "agent-test", {"k": [{"alert": "Dup", "expr": "test"}]}),
            ]
        }
        rows = h.build_crd_rules(payload, "t", "a", "agent-test")
        self.assertEqual(len(rows), 1)
        self.assertFalse(rows[0]["ambiguous"])
        self.assertEqual(rows[0]["expr"], "test")

    def test_null_spec_or_groups_are_skipped(self):
        payload = {
            "items": [
                {"metadata": _meta("a", ["helm"]), "spec": None},
                {"metadata": _meta("b"), "spec": {"groups": None}},
            ]
        }
        self.assertEqual(h.build_crd_rules(payload, "t", "a", None), [])

    def test_null_and_malformed_fields_do_not_crash(self):
        payload = {
            "items": [
                None,
                {"metadata": None, "spec": {"groups": [None, {"name": "g", "rules": None}]}},
                {
                    "metadata": {
                        "name": "x",
                        "namespace": "ns",
                        "labels": None,
                        "annotations": None,
                        "managedFields": None,
                    },
                    "spec": {
                        "groups": [
                            {
                                "name": "g",
                                "rules": [
                                    "junk",
                                    {"alert": "NullMaps", "expr": "1", "labels": None, "annotations": None},
                                    {"alert": "OddSeverity", "expr": "1", "labels": {"severity": 3}},
                                    {"alert": "Critical", "expr": "1", "labels": {"severity": "CRITICAL"}},
                                ],
                            }
                        ]
                    },
                },
            ]
        }
        rows = {r["alert"]: r for r in h.build_crd_rules(payload, "t", "a", None)}
        self.assertEqual(set(rows), {"NullMaps", "OddSeverity", "Critical"})
        self.assertEqual((rows["NullMaps"]["labels"], rows["NullMaps"]["annotations"]), ({}, {}))
        self.assertEqual(rows["NullMaps"]["origin"], "customer_cr")
        self.assertEqual(rows["OddSeverity"]["severity"], "warning")
        self.assertEqual(rows["Critical"]["severity"], "critical")

    def test_api_rules_tolerate_nulls(self):
        api = {
            "groups": [
                None,
                {"name": "g", "rules": None},
                {
                    "name": "g2",
                    "rules": [{"type": "alerting", "name": "A", "query": "1", "labels": None, "annotations": None}],
                },
            ]
        }
        with mock.patch.object(h.database, "insert_data") as insert:
            rows = h.handle_api_based_rules(api, "t", "a")
        self.assertEqual([r["alert"] for r in rows], ["A"])
        self.assertEqual(rows[0]["severity"], "warning")
        insert.assert_called_once()

    def test_rules_without_alert_are_skipped(self):
        payload = {"items": [_cr("a", "ns", {"g": [{"record": "r:x", "expr": "1"}]})]}
        self.assertEqual(h.build_crd_rules(payload, "t", "a", None), [])


class ConflictClauseTest(unittest.TestCase):
    def test_api_pass_leaves_crd_owned_columns(self):
        # enabled too: the rules API lags a PrometheusRule edit, so a sync right
        # after a disable would switch the rule back on.
        for col in ("namespace", "name", '"group"', "is_editable", "origin", "ambiguous", "provider_config", "enabled"):
            self.assertNotIn(f"{col}=EXCLUDED", h.api_rule_on_conflict)

    def test_crd_pass_owns_location_state_and_merges_config(self):
        for col in ('"group"', "enabled", "is_editable", "origin", "ambiguous", "namespace", "name"):
            self.assertIn(f"{col}=EXCLUDED.{col}", h.crd_rule_on_conflict)
        self.assertIn(
            "COALESCE(event_rules.provider_config, '{}'::jsonb) || EXCLUDED.provider_config", h.crd_rule_on_conflict
        )
        self.assertNotIn("origin", h._crd_rule_on_conflict_legacy)


class HandleCrdTest(unittest.TestCase):
    def test_falls_back_without_v941_columns(self):
        payload = {"items": [_cr("a", "ns", {"g": [{"alert": "A", "expr": "1"}]})]}
        calls = []

        def fake_insert(table, rows, on_conflict=""):
            calls.append((rows, on_conflict))
            if len(calls) == 1:
                raise _UndefinedColumn("column origin does not exist")

        with mock.patch.object(h.database, "insert_data", side_effect=fake_insert), mock.patch.object(
            h, "_install_namespace", return_value=None
        ), mock.patch.object(h.psycopg2.errors, "UndefinedColumn", _UndefinedColumn):
            h.handle_crd_based_rules(payload, "t", "a")
        self.assertEqual(len(calls), 2)
        legacy_rows, legacy_clause = calls[1]
        self.assertNotIn("origin", legacy_rows[0])
        self.assertNotIn("ambiguous", legacy_rows[0])
        self.assertEqual(legacy_clause, h._crd_rule_on_conflict_legacy)


if __name__ == "__main__":
    unittest.main()
