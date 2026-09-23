"""Discovery cleanup must close ONLY the events of resources it just deactivated.

Regression cover for the sweep that closed an account's entire open event list on
every snapshot: it compared `events.service_key` against `active_resources.resourse_id`,
two identifiers that never share a format, so `service_key != ALL(active)` matched every
row -- live resources included. Observed live: a pod in CrashLoopBackOff had all four of
its events (agent, datadog, pagerduty) closed by a *Job* discovery pass while the pod was
still crashing.

Runs offline: heavy deps are stubbed before importing the handler, same as
test_resolved_event_notify.py.
"""

import os
import sys
import unittest
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

for _mod in (
    "redis",
    "psycopg2",
    "psycopg2.extras",
    "psycopg2.pool",
    "clickhouse_driver",
    "clickhouse_connect",
    "clickhouse_connect.driver",
    "clickhouse_connect.driver.client",
    "clickhouse_connect.driver.query",
    "clickhouse_connect.driver.summary",
):
    sys.modules.setdefault(_mod, mock.MagicMock())

_APP_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if _APP_DIR not in sys.path:
    sys.path.insert(0, _APP_DIR)

import handlers.discovery_handler as dh  # noqa: E402
from datetime import datetime, timedelta, timezone  # noqa: E402

ACCOUNT = "acct-1"
TENANT = "tenant-1"
# The Deployment that is alive and whose pod is crashlooping.
LIVE_RESOURCE = "a61d9787-d0d4-56dc-95fa-05989036fadd"
GONE_RESOURCE = "b0000000-0000-0000-0000-00000000dead"


class _Recorder:
    """Stands in for database.run_query, remembering every statement executed."""

    def __init__(self, deactivated):
        self.deactivated = deactivated
        self.queries = []

    def __call__(self, query, values=None, cursor_factory=None):
        text = query if isinstance(query, str) else str(query)
        self.queries.append((" ".join(text.split()), values))
        if "UPDATE cloud_resourses" in text and "RETURNING id" in text:
            return [(r,) for r in self.deactivated]
        return []

    def statements(self, needle):
        return [q for q, _ in self.queries if needle in q]


class TestDiscoveryCleanupScoping(unittest.TestCase):
    def _run(self, resource_type, deactivated, active_resources=None):
        rec = _Recorder(deactivated)
        active = active_resources or [(LIVE_RESOURCE, f"ns/{resource_type}/live")]
        with mock.patch.object(dh.database, "run_query", side_effect=rec) as run_query, mock.patch.object(
            dh, "close_events_with_history"
        ) as closer:
            # First call is the active_resources SELECT; the recorder returns [] for it,
            # so feed the active set through a side_effect wrapper instead.
            def _dispatch(query, values=None, cursor_factory=None):
                text = query if isinstance(query, str) else str(query)
                if "FROM active_resources" in text:
                    rec.queries.append((" ".join(text.split()), values))
                    return active
                return rec(query, values, cursor_factory)

            run_query.side_effect = _dispatch
            dh.handle_active_resources_deletion(ACCOUNT, TENANT, resource_type, total_resources_count=1)
        return rec, closer

    def test_no_event_is_closed_by_service_key(self):
        """The service_key predicate is gone -- it could never match across formats."""
        rec, _ = self._run("Job", deactivated=[])
        for stmt in rec.statements("UPDATE events"):
            self.assertNotIn("service_key", stmt)

    def test_live_resource_keeps_its_events(self):
        """A pass that deactivates nothing must not touch the events table at all."""
        rec, closer = self._run("Job", deactivated=[])
        closer.assert_not_called()
        self.assertEqual(rec.statements("UPDATE events"), [])

    def test_close_is_keyed_on_the_deactivated_resource_ids(self):
        _, closer = self._run("Pod", deactivated=[GONE_RESOURCE])
        closer.assert_called_once()
        kwargs = closer.call_args.kwargs
        self.assertIn("cloud_resource_id", kwargs["where_conditions"])
        self.assertNotIn("service_key", kwargs["where_conditions"])
        self.assertEqual(kwargs["params"], [[GONE_RESOURCE]])
        self.assertEqual(kwargs["closing_reason"], "resource_inactive")
        self.assertEqual(kwargs["metadata"]["resource_type"], "Pod")


class TestWorkloadRecoveryClose(unittest.TestCase):
    """The recovery close is DEFAULT ON (see Configs.EVENT_CLOSE_ON_WORKLOAD_RECOVERY).

    Readiness on its own is a single sample and closed live crashloops: measured on
    dev, 39 of the 43 events it closed went within ten minutes of being raised and 17
    re-fired within two hours. It is on anyway, by product decision -- without it the
    agent's findings have no resolve path at all. These tests pin the three checks
    that bound that: the workload counts, the per-pod readiness (a stuck rollout),
    and the restart delta against the previous snapshot (a live crashloop, which the
    first two cannot see because both read the same snapshot).
    """

    def setUp(self):
        enabled = mock.patch.object(dh.Configs, "EVENT_CLOSE_ON_WORKLOAD_RECOVERY", True)
        enabled.start()
        self.addCleanup(enabled.stop)
        self.unready_rows = []
        pods = mock.patch.object(dh.database, "run_query", side_effect=lambda *a, **k: self.unready_rows)
        self.pod_query = pods.start()
        self.addCleanup(pods.stop)

    # utc_from_epoch_millis() returns NAIVE UTC and WorkloadDetails.last_seen carries
    # its .isoformat(), so naive is the shape production actually delivers.
    OBSERVED_AT = datetime(2026, 8, 19, 8, 30)

    def _workload(self, resource_id, total, ready, last_seen="default", name="web"):
        w = mock.Mock()
        w.cloud_resource_id = resource_id
        w.namespace = "default"
        w.name = name
        w.total_pods = total
        w.ready_pods = ready
        w.last_seen = self.OBSERVED_AT.isoformat() if last_seen == "default" else last_seen
        return w

    def test_all_pods_ready_closes_agent_events(self):
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 2, 2)])
        closer.assert_called_once()
        kwargs = closer.call_args.kwargs
        self.assertEqual(kwargs["params"], [[LIVE_RESOURCE], self.OBSERVED_AT])
        self.assertEqual(kwargs["closing_reason"], "workload_recovered")
        self.assertIn("kubernetes_api_server", kwargs["where_conditions"])
        # Config-change findings describe a past event, not a recoverable condition:
        # a healthy workload is their normal state, so recovery must not touch them.
        self.assertIn("finding_type = 'issue'", kwargs["where_conditions"])
        # The close is anchored to when the agent OBSERVED the workload healthy,
        # so an event that started after the snapshot survives it.
        self.assertIn("starts_at < %s", kwargs["where_conditions"])

    def test_close_is_anchored_to_the_oldest_observation(self):
        """A mixed batch must not close on evidence newer than its oldest snapshot."""
        older = self.OBSERVED_AT - timedelta(minutes=20)
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT,
                [
                    self._workload(LIVE_RESOURCE, 1, 1),
                    self._workload(GONE_RESOURCE, 1, 1, last_seen=older.isoformat()),
                ],
            )
        self.assertEqual(closer.call_args.kwargs["params"][1], older)

    def test_timezone_aware_input_is_normalised_to_naive_utc(self):
        """An aware value must not reach a `timestamp` column, and must not break min()."""
        aware = self.OBSERVED_AT.replace(tzinfo=timezone.utc) + timedelta(hours=2)
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT,
                [
                    self._workload(LIVE_RESOURCE, 1, 1),
                    self._workload(GONE_RESOURCE, 1, 1, last_seen=aware.isoformat()),
                ],
            )
        observed = closer.call_args.kwargs["params"][1]
        self.assertIsNone(observed.tzinfo)
        self.assertEqual(observed, self.OBSERVED_AT)

    def test_missing_observation_time_closes_nothing(self):
        """utc_from_epoch_millis(0) means the agent sent no update_time -- fail closed."""
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT,
                [self._workload(LIVE_RESOURCE, 1, 1, last_seen="1970-01-01T00:00:00+00:00")],
            )
        closer.assert_not_called()

    def test_crashlooping_workload_is_not_recovered(self):
        """0/1 pods ready is exactly the crashloop case -- it must stay open."""
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 1, 0)])
        closer.assert_not_called()

    def test_stuck_rollout_is_not_recovered(self):
        """spec.replicas == readyReplicas while a new pod is stuck in ImagePullBackOff.

        The old ReplicaSet keeps the Deployment at 2/2, so the counts say healthy; the
        unready pod row is what says otherwise, and only that workload is held open.
        """
        self.unready_rows = [("default", "web")]
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT,
                [
                    self._workload(LIVE_RESOURCE, 2, 2),
                    self._workload(GONE_RESOURCE, 1, 1, name="api"),
                ],
            )
        closer.assert_called_once()
        self.assertEqual(closer.call_args.kwargs["params"][0], [GONE_RESOURCE])
        query, params = self.pod_query.call_args.args
        self.assertIn("FROM k8s_pods", query)
        self.assertIn("is_active", query)
        # A pod's ready_pods counts containers and total_pods is always 1, so the
        # comparison has to be against the spec's container count.
        self.assertIn("jsonb_array_length(meta->'config'->'containers')", query)
        self.assertEqual(params[0], ACCOUNT)

    def test_only_unready_workload_closes_nothing(self):
        self.unready_rows = [("default", "web")]
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 2, 2)])
        closer.assert_not_called()

    def test_same_name_in_another_namespace_does_not_block(self):
        self.unready_rows = [("staging", "web")]
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 2, 2)])
        closer.assert_called_once()

    def test_recent_restart_keeps_a_ready_workload_open(self):
        """The crashloop-between-restarts case: every single-snapshot check says healthy.

        Counts read 1/1, the pod row from the same batch reads all containers ready,
        and only the restart delta against the previous snapshot says otherwise.
        """
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT, [self._workload(LIVE_RESOURCE, 1, 1)], {("default", "web")}
            )
        closer.assert_not_called()

    def test_restart_in_one_workload_does_not_hold_another(self):
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT,
                [
                    self._workload(LIVE_RESOURCE, 1, 1),
                    self._workload(GONE_RESOURCE, 1, 1, name="api"),
                ],
                {("default", "web")},
            )
        closer.assert_called_once()
        self.assertEqual(closer.call_args.kwargs["params"][0], [GONE_RESOURCE])

    def test_restart_in_another_namespace_does_not_block(self):
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(
                ACCOUNT, [self._workload(LIVE_RESOURCE, 1, 1)], {("staging", "web")}
            )
        closer.assert_called_once()

    def test_no_restart_view_keeps_the_previous_behaviour(self):
        """A caller with no previous-snapshot view still closes on readiness alone."""
        for restarts in (None, set()):
            with self.subTest(restarts=restarts):
                with mock.patch.object(dh, "close_events_with_history") as closer:
                    dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 1, 1)], restarts)
                closer.assert_called_once()

    def test_scaled_to_zero_is_not_recovered(self):
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 0, 0)])
        closer.assert_not_called()

    def test_flag_off_disables_the_close(self):
        with mock.patch.object(dh, "close_events_with_history") as closer, mock.patch.object(
            dh.Configs, "EVENT_CLOSE_ON_WORKLOAD_RECOVERY", False
        ):
            dh.close_events_for_recovered_workloads(ACCOUNT, [self._workload(LIVE_RESOURCE, 1, 1)])
        closer.assert_not_called()

    def test_flag_is_on_by_default(self):
        """Pins the deliberate default. Flipping it is a product decision with a
        measured false-close rate on both sides -- off means agent findings never
        resolve, on means some live crashloops close early -- so neither value may
        change silently as a side effect of another edit."""
        from config import Settings

        self.assertTrue(Settings.model_fields["EVENT_CLOSE_ON_WORKLOAD_RECOVERY"].default)


class TestWorkloadsWithNewPodRestarts(unittest.TestCase):
    """The only check in the recovery close that spans two snapshots."""

    def _pod(self, name, restart_count, namespace="default", workload_name="web"):
        p = mock.Mock()
        p.name = name
        p.namespace = namespace
        p.workload_name = workload_name
        p.restart_count = restart_count
        return p

    def _run(self, pods, stored_rows):
        with mock.patch.object(dh.database, "run_query", return_value=stored_rows):
            return dh._workloads_with_new_pod_restarts(ACCOUNT, pods)

    def test_restart_count_increase_flags_the_workload(self):
        got = self._run([self._pod("web-0", {"app": 4})], [("default", "web-0", {"app": 3})])
        self.assertEqual(got, {("default", "web")})

    def test_unchanged_count_does_not_flag(self):
        got = self._run([self._pod("web-0", {"app": 3})], [("default", "web-0", {"app": 3})])
        self.assertEqual(got, set())

    def test_sums_across_containers(self):
        """A sidecar restarting is still a restart of the pod."""
        got = self._run(
            [self._pod("web-0", {"app": 2, "sidecar": 5})],
            [("default", "web-0", {"app": 2, "sidecar": 4})],
        )
        self.assertEqual(got, {("default", "web")})

    def test_lower_count_does_not_flag(self):
        """A removed container takes its count out of the sum; that is not a restart."""
        got = self._run(
            [self._pod("web-0", {"app": 2})],
            [("default", "web-0", {"app": 2, "sidecar": 7})],
        )
        self.assertEqual(got, set())

    def test_first_sighting_with_restarts_flags(self):
        """No stored row means no way to date the restarts, so assume they are recent.

        Costs one snapshot of the event staying open; the next snapshot compares
        normally. Holding an event open too long is the recoverable direction.
        """
        got = self._run([self._pod("web-0", {"app": 2})], [])
        self.assertEqual(got, {("default", "web")})

    def test_first_sighting_without_restarts_does_not_flag(self):
        got = self._run([self._pod("web-0", {"app": 0})], [])
        self.assertEqual(got, set())

    def test_bare_pods_are_skipped(self):
        """A pod with no workload gates no workload's close."""
        got = self._run([self._pod("loose", {"app": 9}, workload_name=None)], [])
        self.assertEqual(got, set())

    def test_unusable_restart_counts_are_ignored(self):
        for value in (None, "3", {"app": "3"}, {"app": None}):
            with self.subTest(value=value):
                self.assertEqual(self._run([self._pod("web-0", value)], []), set())

    def test_unusable_stored_count_falls_back_to_first_sighting(self):
        got = self._run([self._pod("web-0", {"app": 1})], [("default", "web-0", None)])
        self.assertEqual(got, {("default", "web")})

    def test_no_pods_makes_no_query(self):
        with mock.patch.object(dh.database, "run_query") as q:
            self.assertEqual(dh._workloads_with_new_pod_restarts(ACCOUNT, []), set())
        q.assert_not_called()

    def test_query_is_scoped_to_the_account_and_the_incoming_pods(self):
        with mock.patch.object(dh.database, "run_query", return_value=[]) as q:
            dh._workloads_with_new_pod_restarts(ACCOUNT, [self._pod("web-0", {"app": 0})])
        query, params = q.call_args.args
        self.assertIn("FROM k8s_pods", query)
        self.assertIn("restart_count", query)
        self.assertEqual(params[0], ACCOUNT)
        self.assertEqual(params[1], ["default"])
        self.assertEqual(params[2], ["web-0"])

    def test_runs_before_the_pod_upsert(self):
        """Ordering is the whole correctness argument: the upsert overwrites the
        counts this reads, so reading after it would compare a value with itself and
        never see a restart."""
        calls = []
        pod = self._pod("web-0", {"app": 1})
        workload = mock.Mock(tenant_id=TENANT, namespace="default", name="web", total_pods=1, ready_pods=1)
        with mock.patch.object(
            dh, "_workloads_with_new_pod_restarts", side_effect=lambda *a: calls.append("read")
        ), mock.patch.object(
            dh.database, "insert_data", side_effect=lambda table, *a, **k: calls.append(table)
        ), mock.patch.object(
            dh, "asdict", return_value={}
        ), mock.patch.object(
            dh, "_clean_stale_workloads"
        ), mock.patch.object(
            dh, "_deduplicate_workloads_by_identity", return_value=[workload]
        ), mock.patch.object(
            dh, "close_events_for_recovered_workloads"
        ):
            dh.process_services_updates(ACCOUNT, [pod], [workload])
        self.assertEqual(calls[:2], ["read", "k8s_pods"])


class TestLegacyStatusUpdate(unittest.TestCase):
    def test_running_is_not_treated_as_resolved(self):
        """phase=Running is what a CrashLoopBackOff pod reports between restarts."""
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.run_status_update([{"service_key": "ns/app", "new_status": "Running"}], TENANT, ACCOUNT)
        closer.assert_not_called()

    def test_succeeded_still_resolves(self):
        with mock.patch.object(dh, "close_events_with_history") as closer:
            dh.run_status_update([{"service_key": "ns/app", "new_status": "Succeeded"}], TENANT, ACCOUNT)
        closer.assert_called_once()


if __name__ == "__main__":
    unittest.main()


class TestDeletedResourceClose(unittest.TestCase):
    """process_deleted_resources must close the deleted resource's events.

    Regression cover for #38238: the close matched on `service_key` alone, which the
    Go agent's discovery writes as `<ns>/<Kind>/<name>` while the agent's own findings
    write `<ns>/<name>`. The two never matched, so a deleted workload kept its alerts
    open forever -- 2,288 of them on production against 1,509 resources that no longer
    existed, and only 5 `resource_deleted` closes in the lifetime of the table.
    """

    # `_id` = uuid5(service_key + account), written as both cloud_resourses.id and
    # external_resource_id by process_service_discovery.
    GONE_ID = "b0000000-0000-0000-0000-00000000dead"
    # What discovery calls the resource...
    DISCOVERY_KEY = "ns/Deployment/report-worker"

    def _run(self, deleted):
        rec = _Recorder([])
        with mock.patch.object(dh.database, "run_query", side_effect=rec), mock.patch.object(
            dh, "close_events_with_history"
        ) as closer:
            dh.process_deleted_resources(ACCOUNT, deleted, TENANT)
        return rec, closer

    def test_close_is_keyed_on_the_resource_id(self):
        """The id arm is what makes the close work across the two key formats."""
        _, closer = self._run({self.GONE_ID: self.DISCOVERY_KEY})
        closer.assert_called_once()
        kwargs = closer.call_args.kwargs
        self.assertIn("cloud_resource_id = ANY(%s::uuid[])", kwargs["where_conditions"])
        self.assertEqual(kwargs["params"][0], [self.GONE_ID])
        self.assertEqual(kwargs["closing_reason"], "resource_deleted")

    def test_service_key_arm_is_retained_for_robusta_clusters(self):
        """Robusta writes both sides in the same format; that arm still closes there."""
        _, closer = self._run({self.GONE_ID: self.DISCOVERY_KEY})
        kwargs = closer.call_args.kwargs
        self.assertIn("service_key = ANY(%s)", kwargs["where_conditions"])
        self.assertEqual(kwargs["params"][1], [self.DISCOVERY_KEY])

    def test_close_uses_the_same_ids_as_the_inventory_updates(self):
        """The ids are already correct here -- k8s_pods/k8s_workloads key on them."""
        rec, closer = self._run({self.GONE_ID: self.DISCOVERY_KEY})
        inventory_params = [
            values for stmt, values in rec.queries if "UPDATE k8s_pods" in stmt or "UPDATE k8s_workloads" in stmt
        ]
        self.assertEqual(len(inventory_params), 2)
        for values in inventory_params:
            self.assertEqual(values[-1], closer.call_args.kwargs["params"][0])

    def test_nothing_deleted_touches_nothing(self):
        rec, closer = self._run({})
        closer.assert_not_called()
        self.assertEqual(rec.queries, [])

    def test_emitted_sql_binds_params_in_predicate_order(self):
        """Guards the ordering between the two %s placeholders and params."""
        rec = _Recorder([])
        with mock.patch.object(dh.database, "run_query", side_effect=rec), mock.patch.object(
            dh.database, "insert_data"
        ):
            dh.process_deleted_resources(ACCOUNT, {self.GONE_ID: self.DISCOVERY_KEY}, TENANT)

        updates = [(q, v) for q, v in rec.queries if "UPDATE events SET status = 'CLOSED'" in q]
        self.assertEqual(len(updates), 1)
        statement, values = updates[0]
        self.assertIn(
            "WHERE cloud_account_id = %s AND (cloud_resource_id = ANY(%s::uuid[]) "
            "OR service_key = ANY(%s)) AND status != 'CLOSED'",
            statement,
        )
        self.assertEqual(values, [ACCOUNT, [self.GONE_ID], [self.DISCOVERY_KEY]])
