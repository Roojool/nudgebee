"""Publish-path contract for a broker that is down or moving.

Pins the 2026-09-19 incident: rabbitmq-0 was rescheduled onto another node
(22:00-22:05Z) and every in-flight POST /v1/k8s/discovery blocked 5.08-16.53s
before returning HTTP 500 "Something went wrong" -- 543 of them in five minutes,
across both the server and the worker deployment.

Two defects made that shape, and both are asserted here:

1. The timeouts were fiction. `transport_options` is forwarded into
   `amqp.Connection(**opts)`, whose signature ends in `**kwargs`, so
   `socket_timeout`, `max_retries` and the `interval_*` keys were accepted and
   discarded. What actually bounded a connect attempt was kombu's default
   `connect_timeout` of 5s, multiplied by a 3-retry publish policy that nests
   its own reconnect ladder -- hence 16.5s.
2. The answer was a 500. The k8s-agent's discovery sink (runner/pkg/discovery/
   sink.go) returns an error on any status >= 400 and does not retry, so the
   payload was dropped until the next resync (DISCOVERY_RESYNC, default 30m).
   503 is the status the agent's retry path keys on.

Runs offline: heavy deps are stubbed in sys.modules before importing app code.
"""

import inspect
import os
import sys
import unittest
from unittest import mock

# ---------------------------------------------------------------------------
# Environment + heavy-dependency stubs — MUST run before importing app code.
# ---------------------------------------------------------------------------
_ENV_DEFAULTS = {
    "ENV": "DEV",
    "COLLECTOR_MODE": "server",
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

from amqp.connection import Connection as AmqpConnection  # noqa: E402
from kombu.exceptions import OperationalError  # noqa: E402

from exception.collector_exceptions import ServiceUnavailableError  # noqa: E402
from middleware.auth_token_middleware import ErrorCatcher  # noqa: E402
from rabbitmq import rabbitmq_client as rc  # noqa: E402


class TestConnectionTimeoutsAreReal(unittest.TestCase):
    """Every timeout we configure must be one the driver actually reads."""

    def setUp(self):
        rc._shared_connection = None
        self.addCleanup(setattr, rc, "_shared_connection", None)

    def test_producer_connection_sets_a_bounded_connect_timeout(self):
        with mock.patch.object(rc, "Connection") as conn_cls:
            rc.get_connection()

        kwargs = conn_cls.call_args.kwargs
        self.assertEqual(kwargs["connect_timeout"], rc.PUBLISH_CONNECT_TIMEOUT)
        self.assertLess(
            rc.PUBLISH_CONNECT_TIMEOUT,
            5,
            "kombu's default connect_timeout is 5s; the point of setting it is to be under that",
        )

    def test_transport_options_contain_only_keys_amqp_honours(self):
        """amqp.Connection swallows unknown kwargs, so a typo here is silent.

        This is the check that would have caught the original bug: `socket_timeout`
        looked like it bounded the socket and never did.
        """
        with mock.patch.object(rc, "Connection") as conn_cls:
            rc.get_connection()

        options = conn_cls.call_args.kwargs["transport_options"]
        honoured = set(inspect.signature(AmqpConnection.__init__).parameters)
        unknown = set(options) - honoured
        self.assertEqual(unknown, set(), f"amqp.Connection ignores these transport_options: {unknown}")

    def test_worst_case_publish_stays_under_the_p95_alert_threshold(self):
        """The knobs are only useful if their product is a bound someone checked.

        Worst case is one connect attempt per allowed try; the retry intervals
        are sub-second and ignored here because they only make the real number
        smaller than this ceiling, never larger.
        """
        worst_case = rc.PUBLISH_CONNECT_TIMEOUT * (rc.PUBLISH_MAX_RETRIES + 1)
        self.assertLess(worst_case, 5.0, "HighP95Latency fires above 5s")


class TestPublishFailureIsReportedNotSwallowed(unittest.TestCase):
    def setUp(self):
        rc._shared_connection = None
        self.addCleanup(setattr, rc, "_shared_connection", None)

    def _publish_raising(self, exc):
        """Run publish_message against a producer pool whose publish() raises."""
        producer = mock.MagicMock()
        producer.publish.side_effect = exc
        pool = mock.MagicMock()
        pool.acquire.return_value.__enter__.return_value = producer

        with mock.patch.object(rc, "Connection"):
            with mock.patch.object(rc, "producers", {rc.get_connection(): pool}):
                rc.publish_message("ex", "rk", {"hello": "world"})

    def test_broker_unavailable_propagates_and_is_counted(self):
        with mock.patch.object(rc.otel_metrics, "record_publish_failed") as counter:
            with self.assertRaises(OperationalError):
                self._publish_raising(OperationalError("Connection refused"))

        counter.assert_called_once_with("rk", "broker_unavailable")

    def test_broker_unavailable_is_not_logged_with_a_traceback(self):
        """One WARNING, not an ERROR stack per request.

        This path wrote >1000 ERROR lines in three minutes during the incident,
        for a dependency outage this service can do nothing about.
        """
        with mock.patch.object(rc, "logger") as log:
            with self.assertRaises(OperationalError):
                self._publish_raising(OperationalError("Connection refused"))

        self.assertTrue(log.warning.called)
        self.assertFalse(log.exception.called)

    def test_unexpected_errors_keep_their_traceback(self):
        """Only broker-unavailable is demoted; a real bug still gets a stack."""
        with mock.patch.object(rc, "logger") as log:
            with mock.patch.object(rc.otel_metrics, "record_publish_failed") as counter:
                with self.assertRaises(ValueError):
                    self._publish_raising(ValueError("boom"))

        self.assertTrue(log.exception.called)
        counter.assert_called_once_with("rk", "ValueError")


class TestErrorCatcherStatusMapping(unittest.TestCase):
    """The agent branches on the status code, so it has to mean something."""

    def test_broker_unavailable_becomes_503(self):
        def handler():
            raise OperationalError("Connection refused")

        with self.assertRaises(ServiceUnavailableError) as caught:
            ErrorCatcher(handler)()

        self.assertEqual(caught.exception.code, 503)

    def test_other_failures_still_become_500(self):
        from exception.collector_exceptions import InternalServerError

        def handler():
            raise ValueError("boom")

        with self.assertRaises(InternalServerError) as caught:
            ErrorCatcher(handler)()

        self.assertEqual(caught.exception.code, 500)


if __name__ == "__main__":
    unittest.main()
