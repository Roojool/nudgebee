"""The pod status a human recognises must be stored, not inferred from the phase.

A pod in CrashLoopBackOff has phase `Running` and one in ImagePullBackOff has
phase `Pending`, so the cluster Summary counted every broken pod as healthy and
the Status filter could not select one (#36593). These cover the precedence
kubectl uses, since that is what the counts and the filter now key on.
"""

import os
import sys
from unittest import mock

# Environment + heavy-dependency stubs — MUST run before importing app code
# (same pattern as test_discovery_shape.py).
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

from handlers.discovery_handler import pod_container_status  # noqa: E402


def _waiting(reason, ready=False):
    return {"ready": ready, "state": {"waiting": {"reason": reason}}}


def _terminated(reason, exit_code, ready=False):
    return {"ready": ready, "state": {"terminated": {"reason": reason, "exitCode": exit_code}}}


def _running(ready=True):
    return {"ready": ready, "state": {"running": {"startedAt": "2026-09-20T05:00:00Z"}}}


def test_healthy_pod_has_no_reason():
    assert pod_container_status({"containerStatuses": [_running(), _running()]}) is None


def test_crashloop_is_reported():
    assert pod_container_status({"containerStatuses": [_waiting("CrashLoopBackOff")]}) == "CrashLoopBackOff"


def test_crashloop_between_restarts_reports_the_terminated_half():
    # The same pod alternates between these two snapshots; both must name a problem,
    # otherwise the count flips as discovery samples one half or the other.
    assert pod_container_status({"containerStatuses": [_terminated("Error", 1)]}) == "Error"


def test_container_that_exited_cleanly_is_not_a_problem():
    # A finished sidecar next to a running app container must not read "Completed".
    assert pod_container_status({"containerStatuses": [_terminated("Completed", 0), _running()]}) is None


def test_missing_exit_code_is_treated_as_a_failure():
    assert (
        pod_container_status({"containerStatuses": [{"ready": False, "state": {"terminated": {"reason": "Error"}}}]})
        == "Error"
    )


def test_init_container_wins_over_app_container():
    # kubectl: while an init container is stuck the app containers have not started,
    # and their generic PodInitializing would mask the real reason.
    status_info = {
        "initContainerStatuses": [_waiting("ImagePullBackOff")],
        "containerStatuses": [_waiting("PodInitializing")],
    }
    assert pod_container_status(status_info) == "ImagePullBackOff"


def test_waiting_wins_over_terminated_within_a_group():
    status_info = {"containerStatuses": [_terminated("OOMKilled", 137), _waiting("CrashLoopBackOff")]}
    assert pod_container_status(status_info) == "CrashLoopBackOff"


def test_first_container_wins_when_two_are_broken_the_same_way():
    status_info = {"containerStatuses": [_waiting("ImagePullBackOff"), _waiting("CrashLoopBackOff")]}
    assert pod_container_status(status_info) == "ImagePullBackOff"


def test_missing_status_info_falls_back_to_the_phase():
    # Agents too old to send status_dict, and malformed payloads: None means the
    # reader uses k8s_pods.status.
    assert pod_container_status(None) is None
    assert pod_container_status({}) is None
    assert pod_container_status({"containerStatuses": "not-a-list"}) is None
    assert pod_container_status({"containerStatuses": [None, "junk"]}) is None
    # A malformed "state" must not raise: this runs per pod on every discovery
    # batch, so an AttributeError here would drop the batch.
    assert pod_container_status({"containerStatuses": [{"state": "not-a-dict"}]}) is None
    assert pod_container_status({"containerStatuses": [{"state": {"waiting": "not-a-dict"}}]}) is None
    assert pod_container_status({"containerStatuses": [{"state": {"terminated": ["nope"]}}]}) is None
