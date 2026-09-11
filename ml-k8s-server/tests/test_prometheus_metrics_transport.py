"""What downstream code receives from the api-server metrics transport.

Every consumer in this service -- the rightsizing loaders, the anomaly loader, the HPA
batcher -- was written against the relay's wire shape. The transport now goes through
api-server, which normalises the agent-relay and direct-Prometheus sources into one
response; these tests pin that the normalised response still arrives downstream in the
shape and with the failure behaviour those consumers were written against.
"""

import json
from datetime import datetime, timezone
from unittest.mock import MagicMock, patch

import pytest

from server.metrics.prometheus_metrics import fetch_metrics

QUERY = "kube_replicaset_status_replicas{ __CLUSTER__ }"
START = datetime(2026, 9, 11, 10, 0, 0, tzinfo=timezone.utc)
END = datetime(2026, 9, 11, 11, 0, 0, tzinfo=timezone.utc)

METRIC = {"namespace": "prod", "pod": "api-0"}
TIMESTAMPS = [1757584800, 1757588400, 1757592000]
# api-server sends sample values as JSON numbers; MetricsInput declares them as strings.
TYPED_VALUES = [1.0, 2.5, 3.0]
EXPECTED_VALUES = ["1", "2.5", "3"]


def _response(payload=None, error=None, status=200, text=""):
    result = {"query_key": "q0", "payload": payload if payload is not None else []}
    if error is not None:
        result["error"] = error
    response = MagicMock()
    response.status_code = status
    body = {"results": [result]} if status == 200 else {"message": text}
    response.json.return_value = body
    response.text = text or json.dumps(body)
    return response


def _series_payload():
    return [{"metric": METRIC, "timestamps": TIMESTAMPS, "values": TYPED_VALUES}]


def _fetch(response, **kwargs):
    session = MagicMock()
    session.request.return_value = response
    with patch("server.metrics.prometheus_metrics.resolve_tenant_id", return_value="tenant-1"), patch(
        "server.metrics.api_server_metrics.get_http_session_with_retry", return_value=session
    ):
        params = {
            "queries_obj": [QUERY],
            "start_time": START,
            "end_time": END,
            "duration": None,
            "step": "1h",
            "account_id": "acct-1",
            "tenant_id": "tenant-1",
        }
        params.update(kwargs)
        return fetch_metrics(**params)


class TestDownstreamShape:
    def test_series_arrive_as_metrics_input(self):
        result = _fetch(_response(_series_payload()))
        assert len(result) == 1
        assert result[0].metric == METRIC
        assert result[0].timestamps == TIMESTAMPS
        # Values must be strings, and integral ones must not gain a ".0":
        # ReplicasCount.get_data calls int() on them directly.
        assert result[0].values == EXPECTED_VALUES

    def test_raw_returns_the_flat_series_shape(self):
        # custom_query_range hands this straight to the rightsizing loaders, which read
        # `timestamps` and `values` as sibling arrays.
        result = _fetch(_response(_series_payload()), raw=True)
        assert result == [{"metric": METRIC, "timestamps": TIMESTAMPS, "values": EXPECTED_VALUES}]

    def test_instant_raw_returns_the_nested_vector_shape(self):
        # Prometheus.custom_query re-maps this into {"metric", "value": [ts, val]}.
        result = _fetch(_response(_series_payload()), raw=True, instant=True)
        assert result == [{"metric": METRIC, "value": {"timestamp": TIMESTAMPS[-1], "value": "3"}}]


class TestFailureBehaviourUnchanged:
    def test_agent_not_connected_returns_empty(self):
        # Never an error here: "no agent yet" has always meant "no data", and raising
        # would make the rightsizing scan treat live workloads as deleted.
        result = _fetch(_response(status=400, text=json.dumps({"message": "agent not connected"})))
        assert result == []

    def test_query_error_raises_when_not_raw(self):
        with pytest.raises(ValueError):
            _fetch(_response(error="bad query"))

    def test_query_error_is_swallowed_when_raw(self):
        # raw callers have always taken [] over an exception; the rightsizing loaders
        # rely on it.
        assert _fetch(_response(error="bad query"), raw=True) == []

    def test_transport_failure_raises_when_not_raw(self):
        response = _response(status=500, text=json.dumps({"message": "boom"}))
        with pytest.raises(ValueError):
            _fetch(response)


class TestHPAFailsLoudly:
    """The HPA forecast path raises on any fetch failure, as it always has.

    It has no agent-not-connected special case: a disconnected agent must not be
    recorded as "no data", or the queue message succeeds having written a forecast
    built from nothing.
    """

    def _hpa(self):
        from server.metrics.prometheus_metrics_hpa import PrometheusMetricsHPA

        with patch("server.metrics.prometheus_metrics_hpa.get_prediction_time_range", return_value="1h"), patch(
            "server.metrics.prometheus_metrics_hpa.resolve_tenant_id", return_value="tenant-1"
        ):
            return PrometheusMetricsHPA(
                deployment_name="api", namespace_name="prod", account_id="acct-1", tenant_id="tenant-1"
            )

    def test_agent_not_connected_raises(self):
        from server.metrics.api_server_metrics import AgentNotConnectedError

        hpa = self._hpa()
        with patch(
            "server.metrics.prometheus_metrics_hpa.query_metrics_via_api_server",
            side_effect=AgentNotConnectedError("agent not connected", 400),
        ):
            with pytest.raises(ValueError):
                hpa.fetch_metrics(["up"])

    def test_query_error_raises(self):
        from server.metrics.api_server_metrics import PromQLQueryError

        hpa = self._hpa()
        with patch(
            "server.metrics.prometheus_metrics_hpa.query_metrics_via_api_server",
            side_effect=PromQLQueryError("bad query", 200),
        ):
            with pytest.raises(ValueError):
                hpa.fetch_metrics(["up"])


class TestConcurrencySemaphore:
    def test_distinct_limits_get_distinct_semaphores(self):
        import server.recommendation.vertical_rightsizing.services.metrics_prometheus_service as svc

        svc._metrics_semaphores.clear()
        assert svc._get_relay_semaphore(10) is not svc._get_relay_semaphore(3)

    def test_same_limit_is_reused(self):
        import server.recommendation.vertical_rightsizing.services.metrics_prometheus_service as svc

        svc._metrics_semaphores.clear()
        assert svc._get_relay_semaphore(7) is svc._get_relay_semaphore(7)


class TestTenantResolution:
    def test_null_tenant_is_not_cached_as_the_string_none(self):
        """A NULL tenant column must resolve to "" -- str(None) is "None", which is
        truthy and would be sent as the x-tenant-id header."""
        from server.utils import utils

        utils._tenant_id_cache.clear()
        engine = MagicMock()
        conn = engine.connect.return_value.__enter__.return_value
        conn.execute.return_value = [(None,)]
        with patch.object(utils.DatabaseEngine, "get_engine", return_value=engine):
            assert utils.resolve_tenant_id("acct-1") == ""
        assert "acct-1" not in utils._tenant_id_cache

    def test_real_tenant_is_returned_and_cached(self):
        from server.utils import utils

        utils._tenant_id_cache.clear()
        engine = MagicMock()
        conn = engine.connect.return_value.__enter__.return_value
        conn.execute.return_value = [("tenant-9",)]
        with patch.object(utils.DatabaseEngine, "get_engine", return_value=engine):
            assert utils.resolve_tenant_id("acct-1") == "tenant-9"
        assert utils._tenant_id_cache["acct-1"] == "tenant-9"


class TestConcurrencyClamp:
    def test_non_positive_limit_is_clamped_to_one(self):
        # Semaphore(0) blocks forever; a mistyped override must not hang every scan.
        import server.recommendation.vertical_rightsizing.services.metrics_prometheus_service as svc

        svc._metrics_semaphores.clear()
        assert svc._get_relay_semaphore(0)._value == 1
        assert svc._get_relay_semaphore(-5)._value == 1
