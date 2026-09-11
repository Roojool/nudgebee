"""Correlated-change evidence must survive the restart that follows a config edit.

The shape this guards: someone edits a ConfigMap value, the workload restarts so
the new value is actually read, and the workload OOMs hours later. The restart is
a configuration change too, and a newer one — so a query that returns only the
most recent change hands the reader "a deployment happened" and drops the edit
that caused it.

Also guards the ConfigMap half of the join: an `envFrom` value appears nowhere in
the consuming pod template, so nothing links the two by name. The link comes from
the knowledge graph's USES_CONFIG edges.

Runs offline: heavy deps are stubbed before importing the handler, same as
test_prometheus_subject_backstop.py.
"""

import os
import sys
import unittest
from datetime import datetime, timedelta, timezone
from unittest import mock

_ENV_DEFAULTS = {
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
}
for _k, _v in _ENV_DEFAULTS.items():
    os.environ.setdefault(_k, _v)

for _mod in ("redis", "psycopg2", "psycopg2.extras", "psycopg2.pool", "clickhouse_driver"):
    sys.modules.setdefault(_mod, mock.MagicMock())

_APP_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if _APP_DIR not in sys.path:
    sys.path.insert(0, _APP_DIR)

import handlers.event_handler as eh  # noqa: E402

TENANT = "tenant-1"
ACCOUNT = "account-1"


ALERT_STARTS_AT = "2026-09-10T14:05:00Z"


def _oom_finding():
    """An OOMKilled finding on sessions-api, as the agent emits it."""
    return {
        "finding_type": "issue",
        "aggregation_key": "pod_oom_killer_enricher",
        "service_key": "sessions/sessions-api",
        "subject_namespace": "sessions",
        "subject_name": "sessions-api-7d9f-x2k",
        "subject_type": "pod",
        "starts_at": ALERT_STARTS_AT,
    }


def _data():
    return {"tenant": TENANT, "cloud_account_id": ACCOUNT}


def _is_graph_query(query):
    """The two knowledge-graph lookups, told apart from the events query.

    psycopg2 is stubbed, so the events query is a MagicMock while the graph
    lookups are plain strings.
    """
    return isinstance(query, str) and "knowledge_graph_node" in query


def _events_query_text():
    """The events-query string, as handed to psycopg2's sql.SQL().

    psycopg2 is stubbed in these tests, so sql.SQL() returns a MagicMock and the
    composed query cannot be read back off the run_query call.
    """
    return eh.sql.SQL.call_args[0][0]


class TestConsumedConfigMapNames(unittest.TestCase):
    def test_returns_configmaps_from_the_graph(self):
        with mock.patch.object(eh.database, "run_query", return_value=[("sessions-config",), ("sessions-app",)]) as q:
            names = eh.get_consumed_configmap_names(TENANT, ACCOUNT, "sessions", "sessions-api")
        self.assertEqual(names, ["sessions-config", "sessions-app"])
        # Scoped to the tenant, the account, and the one workload.
        self.assertEqual(q.call_args[0][1], [TENANT, ACCOUNT, "sessions", "sessions-api"])

    def test_graph_failure_is_not_fatal(self):
        # The graph rebuilds hourly and may be mid-build or absent. Losing the
        # ConfigMap half must not lose the workload's own changes too.
        with mock.patch.object(eh.database, "run_query", side_effect=Exception("no graph")):
            self.assertEqual(eh.get_consumed_configmap_names(TENANT, ACCOUNT, "sessions", "sessions-api"), [])

    def test_missing_workload_name_does_not_query(self):
        with mock.patch.object(eh.database, "run_query") as q:
            self.assertEqual(eh.get_consumed_configmap_names(TENANT, ACCOUNT, "sessions", ""), [])
        q.assert_not_called()


class TestRoutedIngressNames(unittest.TestCase):
    """The routing side: which Ingresses send traffic to this workload.

    Name matching can never produce this — in dev, Ingress `app` routes to
    service `notifications`, so the workload and the Ingress fronting it share
    no name at all.
    """

    def test_returns_ingresses_from_the_graph(self):
        with mock.patch.object(eh.database, "run_query", return_value=[("app",)]) as q:
            names = eh.get_routed_ingress_names(TENANT, ACCOUNT, "nudgebee-oss", "notifications")
        self.assertEqual(names, ["app"])
        self.assertEqual(q.call_args[0][1], [TENANT, ACCOUNT, "nudgebee-oss", "notifications"])

    def test_traverses_exposes_backwards(self):
        # EXPOSES is declared "K8sService -> Workload" but written
        # Workload -> K8sService; a query built from the comment silently
        # returns nothing.
        with mock.patch.object(eh.database, "run_query", return_value=[]) as q:
            eh.get_routed_ingress_names(TENANT, ACCOUNT, "nudgebee-oss", "notifications")
        query = q.call_args[0][0]
        self.assertIn("exposes.source_node_id = w.id", query)
        self.assertIn("routes.destination_node_id = svc.id", query)

    def test_graph_failure_is_not_fatal(self):
        with mock.patch.object(eh.database, "run_query", side_effect=Exception("no graph")):
            self.assertEqual(eh.get_routed_ingress_names(TENANT, ACCOUNT, "sessions", "sessions-api"), [])


class TestGetLastConfigChange(unittest.TestCase):
    def test_returns_several_changes_not_just_the_newest(self):
        edit = (1, "sessions/sessions-config", [], datetime.now(timezone.utc) - timedelta(hours=6))
        restart = (2, "sessions/sessions-api", [], datetime.now(timezone.utc) - timedelta(hours=6))

        def fake_query(query, values):
            if _is_graph_query(query):
                return [("sessions-config",)] if "USES_CONFIG" in query else []
            return [restart, edit]

        with mock.patch.object(eh.database, "run_query", side_effect=fake_query):
            rows = eh.get_last_config_change([], _data(), _oom_finding())

        self.assertEqual(len(rows), 2, "the restart must not shadow the edit that caused it")

    def test_query_covers_both_the_workload_and_its_configmaps(self):
        captured = {}

        def fake_query(query, values):
            if _is_graph_query(query):
                if "USES_CONFIG" in query:
                    return [("sessions-config",), ("sessions-app",)]
                return [("sessions-public",)]
            captured["values"] = values
            return []

        with mock.patch.object(eh.database, "run_query", side_effect=fake_query):
            eh.get_last_config_change([], _data(), _oom_finding())

        query = _events_query_text()
        self.assertIn("service_key = %s", query)
        self.assertIn("subject_type = 'configmap'", query)
        self.assertIn("subject_type = 'ingress'", query)
        self.assertIn("sessions/sessions-api", captured["values"])
        self.assertIn("sessions-config", captured["values"])
        self.assertIn("sessions-app", captured["values"])
        self.assertIn("sessions-public", captured["values"])
        # Ordered by when the change happened, not when we ingested it.
        self.assertIn("order by starts_at desc", query)
        # The two sources are OR'd inside one group, so the namespace/tenant
        # conditions still apply to both.
        self.assertIn("and (service_key = %s or (subject_type", query)

    def test_without_graph_edges_the_workload_query_still_runs(self):
        def fake_query(query, values):
            return []

        with mock.patch.object(eh.database, "run_query", side_effect=fake_query):
            eh.get_last_config_change([], _data(), _oom_finding())

        query = _events_query_text()
        self.assertIn("service_key = %s", query)
        # No empty IN () list — that is a syntax error, not an empty result.
        self.assertNotIn("subject_type = 'configmap'", query)
        self.assertNotIn("subject_type = 'ingress'", query)

    def test_change_events_do_not_correlate_against_themselves(self):
        finding = _oom_finding()
        finding["finding_type"] = "configuration_change"
        with mock.patch.object(eh.database, "run_query") as q:
            self.assertEqual(eh.get_last_config_change([], _data(), finding), [])
        q.assert_not_called()

    def test_still_bounded_by_the_alerts_own_start_time(self):
        """A change made after the alert fired cannot be its cause.

        Widening this from one change to several must not lose that bound —
        with a limit of 5, an unbounded query would happily return four changes
        that postdate the alert.
        """
        captured = {}

        def fake_query(query, values):
            if _is_graph_query(query):
                return []
            captured["values"] = values
            return []

        with mock.patch.object(eh.database, "run_query", side_effect=fake_query):
            eh.get_last_config_change([], _data(), _oom_finding())

        self.assertIn("starts_at <= %s", _events_query_text())
        bounds = [v for v in captured["values"] if isinstance(v, datetime)]
        self.assertEqual(len(bounds), 1)
        # Naive UTC: events.starts_at is a `timestamp` column, and a tz-aware
        # bound is converted through the session TimeZone on the way in.
        self.assertIsNone(bounds[0].tzinfo)
        self.assertEqual(bounds[0], datetime(2026, 9, 10, 14, 5, 0))

    def test_limit_is_the_last_bound_value(self):
        captured = {}

        def fake_query(query, values):
            if _is_graph_query(query):
                return []
            captured["values"] = values
            return []

        with mock.patch.object(eh.database, "run_query", side_effect=fake_query):
            eh.get_last_config_change([], _data(), _oom_finding())

        self.assertEqual(captured["values"][-1], eh.CONFIG_CHANGE_CORRELATION_LIMIT)


class TestFormatEvidenceRendersEveryChange(unittest.TestCase):
    def test_one_diff_block_per_correlated_change(self):
        started = datetime.now(timezone.utc)
        changes = [
            (1, "sessions/sessions-api", [{"type": "diff", "data": {"updated_paths": ["spec.template"]}}], started),
            (
                2,
                "sessions/sessions-config",
                [{"type": "diff", "data": {"updated_paths": ["data.CACHE_TTL_SECONDS"]}}],
                started,
            ),
        ]
        out = eh.format_evidence([], changes, "pod_oom_killer_enricher")
        self.assertEqual(len(out), 2)
        paths = [block["data"]["updated_paths"] for block in out]
        self.assertIn(["data.CACHE_TTL_SECONDS"], paths, "the ConfigMap edit must reach the evidence")

    def test_one_broken_change_does_not_drop_the_others(self):
        started = datetime.now(timezone.utc)
        changes = [
            # evidences is JSONB; a row holding a scalar there is not iterable
            # and blows up in get_config_change_data.
            (1, "sessions/sessions-api", 42, started),
            (
                2,
                "sessions/sessions-config",
                [{"type": "diff", "data": {"updated_paths": ["data.CACHE_TTL_SECONDS"]}}],
                started,
            ),
        ]
        out = eh.format_evidence([], changes, "pod_oom_killer_enricher")
        self.assertEqual(len(out), 1)

    def test_a_change_with_no_start_time_is_still_rendered(self):
        """`starts_at` is nullable and the query orders `nulls last`.

        With one change returned that only mattered when every candidate row was
        null; returning five means a null-timestamped row can arrive in the tail
        of an otherwise good result, and dropping it would lose a real change.
        """
        changes = [
            (
                1,
                "sessions/sessions-config",
                [{"type": "diff", "data": {"updated_paths": ["data.CACHE_TTL_SECONDS"]}}],
                None,
            ),
        ]
        out = eh.format_evidence([], changes, "pod_oom_killer_enricher")
        self.assertEqual(len(out), 1)
        self.assertIsNone(out[0]["start_at"])
        self.assertEqual(out[0]["data"]["updated_paths"], ["data.CACHE_TTL_SECONDS"])


class TestMemoryLimitInsightDirection(unittest.TestCase):
    """The OOM insight must not recommend the move that just failed.

    "Increase memory limit in pod specifications" was emitted whenever a memory
    limit changed before an OOMKill — including when the limit had just been
    raised and the workload hit it anyway, which is precisely when raising it
    again is the wrong answer.
    """

    @staticmethod
    def _insight(old, new):
        config_data = {
            "updated_paths": ["spec.template.spec.containers[0].resources.limits['memory']"],
            "updated_values": [
                {
                    "path": "spec.template.spec.containers[0].resources.limits['memory']",
                    "old": old,
                    "new": new,
                }
            ],
        }
        insights = eh.get_change_insights(config_data, "pod_oom_killer_enricher")
        return insights[0]["recommendation"]

    def test_raised_limit_does_not_recommend_raising_it_again(self):
        rec = self._insight("192Mi", "384Mi")
        self.assertNotIn("Increase memory limit", rec)
        self.assertIn("still hit it", rec)

    def test_lowered_limit_points_at_the_reduction(self):
        self.assertIn("lowered", self._insight("512Mi", "192Mi"))

    def test_unparseable_values_get_neutral_advice(self):
        rec = self._insight(None, "not-a-quantity")
        self.assertIn("Check the workload's memory usage", rec)

    def test_non_oom_events_get_no_recommendation(self):
        config_data = {
            "updated_paths": ["spec.template.spec.containers[0].resources.limits['memory']"],
            "updated_values": [],
        }
        insights = eh.get_change_insights(config_data, "report_crash_loop")
        self.assertNotIn("recommendation", insights[0])


class TestCorrelatedChangeTitles(unittest.TestCase):
    """Each attached change must say which object it is about.

    LastDeploymentCard falls back to the literal "Last Deployment Change" when
    no title is supplied. That was accurate while exactly one change was ever
    attached and it was always the workload's own deploy; with a ConfigMap edit
    and an Ingress annotation attached alongside it, the reader gets several
    identically-titled rows and cannot tell them apart.
    """

    @staticmethod
    def _block(resource_name, started):
        diff = {"resource_name": resource_name, "updated_paths": [], "updated_values": []}
        row = (1, "sessions/sessions-api", [{"type": "diff", "data": diff}], started)
        return eh.get_config_change_data(row, "pod_oom_killer_enricher")

    def test_names_the_changed_object(self):
        started = datetime(2026, 9, 11, 14, 5, 0)
        self.assertEqual(
            self._block("configmap/sessions/sessions-config.yaml", started)["additional_info"]["title"],
            "ConfigMap sessions-config changed · 14:05 UTC",
        )
        self.assertEqual(
            self._block("ingress/sessions/sessions-public.yaml", started)["additional_info"]["title"],
            "Ingress sessions-public changed · 14:05 UTC",
        )
        self.assertEqual(
            self._block("deployment/sessions/sessions-api.yaml", started)["additional_info"]["title"],
            "Deployment sessions-api changed · 14:05 UTC",
        )

    def test_repeat_edits_to_one_object_stay_distinguishable(self):
        first = self._block("configmap/sessions/sessions-config.yaml", datetime(2026, 9, 11, 8, 57, 0))
        second = self._block("configmap/sessions/sessions-config.yaml", datetime(2026, 9, 11, 9, 6, 0))
        self.assertNotEqual(first["additional_info"]["title"], second["additional_info"]["title"])

    def test_unusable_resource_name_adds_no_title(self):
        # Better the card's own fallback than a title built from nothing.
        started = datetime(2026, 9, 11, 14, 5, 0)
        self.assertNotIn("additional_info", self._block("", started))
        self.assertNotIn("additional_info", self._block("bare-name", started))

    def test_missing_timestamp_still_names_the_object(self):
        block = self._block("configmap/sessions/sessions-config.yaml", None)
        self.assertEqual(block["additional_info"]["title"], "ConfigMap sessions-config changed")


if __name__ == "__main__":
    unittest.main()
