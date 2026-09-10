"""Savings contract for pod_right_sizing rows on the KRR-upload path.

Requests are per pod, so a container's monthly delta is scaled by the live
replica count; without it a 20-replica Deployment reports a twentieth of its
real saving. A resource entry without usable numbers contributes nothing and
must not throw the whole container away: a sidecar with no memory series
would otherwise erase the app container's cpu saving from the merged row.
Mirrors test_vertical_rightsizing_savings.py in ml-k8s-server.

Runs offline: heavy deps are stubbed before importing the handler, same as
test_pod_rightsizing_no_change.py.
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

for _mod in ("redis", "psycopg2", "psycopg2.extras", "psycopg2.pool", "clickhouse_driver"):
    sys.modules.setdefault(_mod, mock.MagicMock())

_APP_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if _APP_DIR not in sys.path:
    sys.path.insert(0, _APP_DIR)

from handlers.event_handler import process_resource_recommendation  # noqa: E402

GB = 1024 * 1024 * 1024
CPU_RATE = 0.04
MEM_RATE = 0.005
HOURS_PER_MONTH = 24 * 30
# Empty settings fall back to the 10% change threshold; every delta here is 50%.
SETTINGS = {}


def _entry(resource, allocated, recommended):
    return {
        "resource": resource,
        "allocated": {"request": allocated, "limit": allocated},
        "recommended": {"request": recommended, "limit": recommended},
    }


def _saving(content, pods_count=None):
    if pods_count is None:
        saving, _ = process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1")
    else:
        saving, _ = process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1", pods_count)
    return saving


class TestReplicaMultiplier(unittest.TestCase):
    def test_cpu_savings_scale_with_pod_count(self):
        half_core = 0.5 * CPU_RATE * HOURS_PER_MONTH
        for pods, expected in [(1, half_core), (3, 3 * half_core), (20, 20 * half_core)]:
            with self.subTest(pods=pods):
                self.assertAlmostEqual(_saving([_entry("cpu", 1.0, 0.5)], pods), expected)

    def test_pods_count_defaults_to_one(self):
        self.assertAlmostEqual(_saving([_entry("cpu", 1.0, 0.5)]), 0.5 * CPU_RATE * HOURS_PER_MONTH)

    def test_memory_savings_scale_with_pod_count(self):
        # The recommended request goes through round_memory first, so compare
        # against the single-pod figure rather than a hand-computed delta.
        single = _saving([_entry("memory", 4 * GB, 2 * GB)], 1)
        self.assertGreater(single, 0)
        self.assertAlmostEqual(_saving([_entry("memory", 4 * GB, 2 * GB)], 3), 3 * single)

    def test_zero_or_unreadable_count_means_one_replica(self):
        half_core = 0.5 * CPU_RATE * HOURS_PER_MONTH
        for pods in (0, None, "n/a"):
            with self.subTest(pods=pods):
                self.assertAlmostEqual(_saving([_entry("cpu", 1.0, 0.5)], pods), half_core)

    def test_raising_requests_returns_a_negative(self):
        self.assertAlmostEqual(_saving([_entry("cpu", 0.5, 1.0)], 4), -0.5 * CPU_RATE * HOURS_PER_MONTH * 4)


class TestEntriesWithoutNumbers(unittest.TestCase):
    def test_no_data_entry_contributes_nothing_and_keeps_the_container(self):
        content = [{"resource": "memory", "info": "No data"}, _entry("cpu", 1.0, 0.5)]
        saving, ignore = process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1", 2)
        self.assertAlmostEqual(saving, 0.5 * CPU_RATE * HOURS_PER_MONTH * 2)
        self.assertFalse(ignore)

    def test_not_enough_data_on_every_entry_still_drops_the_container(self):
        content = [{"resource": "cpu", "info": "Not enough data"}, {"resource": "memory", "info": "Not enough data"}]
        saving, ignore = process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1", 5)
        self.assertEqual(saving, 0)
        self.assertTrue(ignore)


# Golden vector shared with the ml-k8s-server twin (test_vertical_rightsizing_savings.py):
# an app container that saves on both resources and a sidecar that pays on both.
# Per pod: app cpu 1.0->0.5 (+$14.40), app memory 4G->2G (+$7.20),
# sidecar cpu 0.1->0.3 (-$5.76), sidecar memory 1G->2G (-$3.60); net +$12.24.
# Memory figures are matched to the cent: this producer rounds the recommended
# memory to a Mi boundary first, the ml-k8s-server twin does not.
MIXED_APP = [_entry("cpu", 1.0, 0.5), _entry("memory", 4 * GB, 2 * GB)]
MIXED_SIDECAR = [_entry("cpu", 0.1, 0.3), _entry("memory", 1 * GB, 2 * GB)]
MIXED_EXPECTED = {
    "app": {"cpu": 14.40, "memory": 7.20},
    "sidecar": {"cpu": -5.76, "memory": -3.60},
}


class TestPerEntryFigures(unittest.TestCase):
    def test_each_entry_keeps_its_own_signed_figure(self):
        for name, content in (("app", MIXED_APP), ("sidecar", MIXED_SIDECAR)):
            with self.subTest(container=name):
                total, _ = process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1", 1)
                by_resource = {e["resource"]: e["add_info"]["estimated_savings"] for e in content}
                for resource, expected in MIXED_EXPECTED[name].items():
                    self.assertAlmostEqual(by_resource[resource], expected, delta=0.01)
                self.assertAlmostEqual(total, sum(MIXED_EXPECTED[name].values()), delta=0.02)

    def test_entry_figures_carry_the_replica_multiplier(self):
        content = [_entry("cpu", 1.0, 0.5)]
        process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1", 3)
        self.assertAlmostEqual(content[0]["add_info"]["estimated_savings"], 3 * 0.5 * CPU_RATE * HOURS_PER_MONTH)

    def test_unpriced_entries_get_no_figure(self):
        content = [{"resource": "memory", "info": "No data"}, _entry("cpu", 1.0, 0.5)]
        process_resource_recommendation(CPU_RATE, MEM_RATE, content, SETTINGS, "res-1", 1)
        self.assertNotIn("add_info", content[0])
        self.assertAlmostEqual(content[1]["add_info"]["estimated_savings"], 0.5 * CPU_RATE * HOURS_PER_MONTH)

    def test_entry_figure_is_added_to_existing_add_info(self):
        entry = _entry("cpu", 1.0, 0.5)
        entry["add_info"] = {"replicas": {"avg": 2}}
        process_resource_recommendation(CPU_RATE, MEM_RATE, [entry], SETTINGS, "res-1", 1)
        self.assertEqual(entry["add_info"]["replicas"], {"avg": 2})
        self.assertAlmostEqual(entry["add_info"]["estimated_savings"], 0.5 * CPU_RATE * HOURS_PER_MONTH)


if __name__ == "__main__":
    unittest.main()


from datetime import datetime, timedelta  # noqa: E402

from handlers.event_handler import replica_stats_from_pod_rows  # noqa: E402

NOW = datetime(2026, 9, 8, 12, 0, 0)
H = timedelta(hours=1)


def _pod(workload, created_hours_ago, last_seen_hours_ago=None, kind="Deployment", namespace="ns"):
    """A pod row: active when last_seen_hours_ago is None."""
    active = last_seen_hours_ago is None
    return (
        kind,
        workload,
        namespace,
        NOW - created_hours_ago * H,
        NOW if active else NOW - last_seen_hours_ago * H,
        active,
    )


class TestReplicaStatsFromPodRows(unittest.TestCase):
    def test_steady_workload_averages_its_replica_count(self):
        rows = [_pod("web", 400), _pod("web", 400), _pod("web", 400)]
        stats = replica_stats_from_pod_rows(rows, NOW)["Deployment/ns/web"]
        self.assertEqual((stats["avg"], stats["min"], stats["max"], stats["window_hours"]), (3, 3, 3, 336))

    def test_scale_out_shows_as_a_range_and_a_weighted_average(self):
        # Two pods for the whole window, eight more for the last quarter of it.
        rows = [_pod("web", 400), _pod("web", 400)] + [_pod("web", 84) for _ in range(8)]
        stats = replica_stats_from_pod_rows(rows, NOW)["Deployment/ns/web"]
        self.assertEqual((stats["min"], stats["max"]), (2, 10))
        self.assertGreater(stats["avg"], 2)
        self.assertLess(stats["avg"], 10)
        self.assertAlmostEqual(stats["avg"], 4.0, delta=0.05)

    def test_young_workload_averages_over_its_own_lifetime(self):
        rows = [_pod("new", 24) for _ in range(10)]
        stats = replica_stats_from_pod_rows(rows, NOW)["Deployment/ns/new"]
        self.assertEqual(stats["avg"], 10)

    def test_churned_incarnations_do_not_stack(self):
        # A 2-replica workload with a rollout every day: old pods stop at their
        # last_seen, so any given hour still counts two.
        rows = []
        for day in range(14):
            rows += [_pod("web", 336 - day * 24, 336 - (day + 1) * 24) for _ in range(2)]
        rows += [_pod("web", 0.5), _pod("web", 0.5)]
        stats = replica_stats_from_pod_rows(rows, NOW)["Deployment/ns/web"]
        self.assertEqual(stats["max"], 2)

    def test_pods_outside_the_window_and_unusable_rows_are_ignored(self):
        rows = [_pod("old", 800, 500), ("Deployment", "bad", "ns", None, NOW, True)]
        self.assertEqual(replica_stats_from_pod_rows(rows, NOW), {})
