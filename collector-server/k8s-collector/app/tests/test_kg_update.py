import unittest
from unittest import mock

from handlers import discovery_handler as dh
from handlers import kg_update

TENANT = "11111111-1111-4111-8111-111111111111"
ACCOUNT = "22222222-2222-4222-8222-222222222222"


def setUpModule():
    """These tests assert what gets published, so they need the gate open.

    KG_UPDATE_NOTIFICATIONS_ENABLED is off by default; TestNotificationsGate
    below covers the closed case.
    """
    global _gate_before
    _gate_before = kg_update.Configs.KG_UPDATE_NOTIFICATIONS_ENABLED
    kg_update.Configs.KG_UPDATE_NOTIFICATIONS_ENABLED = True


def tearDownModule():
    kg_update.Configs.KG_UPDATE_NOTIFICATIONS_ENABLED = _gate_before


class TestNotificationsGate(unittest.TestCase):
    """The flag decides whether anything reaches the broker at all."""

    def setUp(self):
        self._before = kg_update.Configs.KG_UPDATE_NOTIFICATIONS_ENABLED
        self.addCleanup(setattr, kg_update.Configs, "KG_UPDATE_NOTIFICATIONS_ENABLED", self._before)

    def test_disabled_publishes_nothing(self):
        kg_update.Configs.KG_UPDATE_NOTIFICATIONS_ENABLED = False
        with mock.patch.object(kg_update, "publish_message") as publish:
            kg_update.publish_kg_update(TENANT, ACCOUNT)
            kg_update.publish_kg_update(
                TENANT, resources=[{"account_id": ACCOUNT, "provider_resource_id": "wl-1", "op": "delete"}]
            )
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                kg_update.record_deleted_workloads([("wl-1", "Deployment")])
        publish.assert_not_called()

    def test_enabled_publishes(self):
        kg_update.Configs.KG_UPDATE_NOTIFICATIONS_ENABLED = True
        with mock.patch.object(kg_update, "publish_message") as publish:
            kg_update.publish_kg_update(TENANT, ACCOUNT)
        publish.assert_called_once()
        self.assertEqual(publish.call_args.kwargs["message"]["account_ids"], [ACCOUNT])


class TestDeletionScope(unittest.TestCase):
    def test_publishes_recorded_deletions_once(self):
        with mock.patch.object(kg_update, "publish_message") as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                kg_update.record_deleted_workloads([("wl-1", "Deployment"), ("wl-2", "StatefulSet")])
                kg_update.record_deleted_workloads([("wl-1", "Deployment")])

        publish.assert_called_once()
        message = publish.call_args.kwargs["message"]
        self.assertEqual(message["tenant_id"], TENANT)
        self.assertNotIn("account_ids", message, "a delete-refs message must not ask for an account rebuild")
        self.assertEqual(
            message["resources"],
            [
                {"account_id": ACCOUNT, "provider_resource_id": "wl-1", "node_type": "Workload", "op": "delete"},
                {"account_id": ACCOUNT, "provider_resource_id": "wl-2", "node_type": "Workload", "op": "delete"},
            ],
        )

    def test_nothing_recorded_publishes_nothing(self):
        with mock.patch.object(kg_update, "publish_message") as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                kg_update.record_deleted_workloads([])
                kg_update.record_deleted_workloads(None)
        publish.assert_not_called()

    def test_pods_and_jobs_are_not_reported(self):
        with mock.patch.object(kg_update, "publish_message") as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                kg_update.record_deleted_workloads([("pod-1", "Pod"), ("job-1", "Job")])
        publish.assert_not_called()

    def test_recording_outside_a_scope_is_ignored(self):
        with mock.patch.object(kg_update, "publish_message") as publish:
            kg_update.record_deleted_workloads([("wl-1", "Deployment")])
        publish.assert_not_called()

    def test_publishes_even_when_processing_raises(self):
        with mock.patch.object(kg_update, "publish_message") as publish:
            with self.assertRaises(RuntimeError):
                with kg_update.deletion_scope(TENANT, ACCOUNT):
                    kg_update.record_deleted_workloads([("wl-1", "Deployment")])
                    raise RuntimeError("boom")
        publish.assert_called_once()

    def test_node_deletions_become_node_refs(self):
        with mock.patch.object(kg_update, "publish_message") as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                kg_update.record_deleted_workloads([("wl-1", "Deployment")])
                kg_update.record_deleted_nodes([("ip-10-0-0-1",), ("ip-10-0-0-1",), ("",)])

        publish.assert_called_once()
        self.assertEqual(
            publish.call_args.kwargs["message"]["resources"],
            [
                {"account_id": ACCOUNT, "provider_resource_id": "wl-1", "node_type": "Workload", "op": "delete"},
                {"account_id": ACCOUNT, "provider_resource_id": "ip-10-0-0-1", "node_type": "Node", "op": "delete"},
            ],
        )

    def test_publish_failure_is_swallowed(self):
        with mock.patch.object(kg_update, "publish_message", side_effect=RuntimeError("broker down")):
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                kg_update.record_deleted_workloads([("wl-1", "Deployment")])


class TestProcessDeletedResourcesReportsWorkloads(unittest.TestCase):
    def test_deleted_workloads_become_delete_refs(self):
        def fake_run_query(query, values=None, **_kwargs):
            if "UPDATE k8s_workloads" in query:
                self.assertIn("RETURNING cloud_resource_id, kind", query)
                self.assertIn("is_active = true", query)
                return [("wl-1", "Deployment"), ("job-1", "Job")]
            return []

        with mock.patch.object(dh.database, "run_query", side_effect=fake_run_query), mock.patch.object(
            dh, "close_events_with_history"
        ), mock.patch.object(kg_update, "publish_message") as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                dh.process_deleted_resources(ACCOUNT, {"wl-1": "shop/Deployment/api", "job-1": "shop/Job/x"}, TENANT)

        publish.assert_called_once()
        refs = publish.call_args.kwargs["message"]["resources"]
        self.assertEqual([r["provider_resource_id"] for r in refs], ["wl-1"])


class TestNodeDeactivationsReportNames(unittest.TestCase):
    def test_extract_disabled_nodes_records_names(self):
        def fake_run_query(query, values=None, **_kwargs):
            if "UPDATE k8s_nodes" in query:
                self.assertIn("RETURNING name", query)
                self.assertIn("is_active = true", query)
                return [("ip-10-0-0-1",)]
            return []

        with mock.patch.object(dh.database, "run_query", side_effect=fake_run_query), mock.patch.object(
            kg_update, "publish_message"
        ) as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                dh.extract_disabled_nodes(ACCOUNT, {"b2ca6e00-0000-4000-8000-000000000001": "node"}, TENANT)

        publish.assert_called_once()
        refs = publish.call_args.kwargs["message"]["resources"]
        self.assertEqual([(r["provider_resource_id"], r["node_type"]) for r in refs], [("ip-10-0-0-1", "Node")])

    def test_full_load_node_updates_records_names(self):
        queries = []

        def fake_run_query(query, values=None, **_kwargs):
            queries.append(query)
            return [("ip-10-0-0-2",)] if "UPDATE k8s_nodes" in query else []

        with mock.patch.object(dh.database, "run_query", side_effect=fake_run_query), mock.patch.object(
            kg_update, "publish_message"
        ) as publish:
            with kg_update.deletion_scope(TENANT, ACCOUNT):
                dh.handle_full_load_node_updates(TENANT, ACCOUNT, {"b2ca6e00-0000-4000-8000-000000000002": {}})

        self.assertTrue(any("RETURNING name" in q for q in queries if "UPDATE k8s_nodes" in q))
        refs = publish.call_args.kwargs["message"]["resources"]
        self.assertEqual(refs[0]["node_type"], "Node")


if __name__ == "__main__":
    unittest.main()
