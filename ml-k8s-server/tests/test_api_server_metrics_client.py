"""The api-server metrics transport: what we send, and what we make of the answer.

These lock the wire contract that ml-k8s-server has no way to check at runtime -- the
units, the header set, and the shapes every downstream consumer parses.
"""

import json
from datetime import datetime, timedelta, timezone
from unittest.mock import MagicMock, patch

import pytest

from server.metrics.api_server_metrics import (
    AgentNotConnectedError,
    ApiServerMetricsError,
    PromQLQueryError,
    _format_value,
    _step_seconds,
    _to_series_entries,
    _to_vector_entries,
    query_metrics_via_api_server,
)

START = datetime(2026, 9, 11, 10, 0, 0, tzinfo=timezone.utc)
END = datetime(2026, 9, 11, 11, 0, 0, tzinfo=timezone.utc)


def _response(status=200, body=None, text=""):
    response = MagicMock()
    response.status_code = status
    response.text = text or json.dumps(body or {})
    response.json.return_value = body if body is not None else {}
    return response


def _call(response, **kwargs):
    """Run one query against a stubbed session and hand back (result, request kwargs)."""
    session = MagicMock()
    session.request.return_value = response
    with patch("server.metrics.api_server_metrics.get_http_session_with_retry", return_value=session):
        params = {
            "account_id": "acct-1",
            "tenant_id": "tenant-1",
            "query": "kube_pod_info{ __CLUSTER__ }",
            "start_time": START,
            "end_time": END,
        }
        params.update(kwargs)
        result = query_metrics_via_api_server(**params)
    return result, session.request.call_args


class TestRequestEnvelope:
    def test_action_provider_and_history(self):
        _, call = _call(_response(body={"results": []}))
        payload = json.loads(call.kwargs["data"])
        assert payload["action"]["name"] == "metrics_query"
        request = payload["input"]["request"]
        # Naming the provider is what stops a Datadog-default account being handed PromQL.
        assert request["metric_provider"] == "prometheus"
        # Omitted on purpose: setting it would defeat the source resolution entirely.
        assert "metric_provider_source" not in request
        # These are background scans, not something a user typed into the explorer.
        assert request["record_history"] is False
        assert request["queries"] == {"q0": "kube_pod_info{ __CLUSTER__ }"}

    def test_times_are_epoch_milliseconds(self):
        _, call = _call(_response(body={"results": []}))
        request = json.loads(call.kwargs["data"])["input"]["request"]
        # FetchMetricsRequest divides by 1000 on both source paths; seconds here would
        # silently ask for a window in 1970.
        assert request["start_time"] == int(START.timestamp() * 1000)
        assert request["end_time"] == int(END.timestamp() * 1000)
        assert request["start_time"] > 1_000_000_000_000

    def test_naive_datetimes_are_treated_as_utc(self):
        naive_start = START.replace(tzinfo=None)
        naive_end = END.replace(tzinfo=None)
        _, call = _call(_response(body={"results": []}), start_time=naive_start, end_time=naive_end)
        request = json.loads(call.kwargs["data"])["input"]["request"]
        assert request["start_time"] == int(START.timestamp() * 1000)

    def test_headers_carry_tenant_and_token_but_no_user(self):
        _, call = _call(_response(body={"results": []}))
        headers = call.kwargs["headers"]
        assert headers["X-ACTION-TOKEN"] == "test-action-token"
        assert headers["x-tenant-id"] == "tenant-1"
        # Tenant with no user is what makes api-server build a tenant-admin context,
        # which is what clears its per-account metrics read gate.
        assert "x-user-id" not in headers

    def test_url_is_the_rpc_metrics_action(self):
        _, call = _call(_response(body={"results": []}))
        assert call.args[1] == "http://services-server:8000/rpc/metrics"

    def test_no_tenant_refuses_before_making_a_request(self):
        with pytest.raises(ApiServerMetricsError, match="no tenant"):
            query_metrics_via_api_server(account_id="acct-1", tenant_id="", query="up", start_time=START, end_time=END)


class TestStepParsing:
    @pytest.mark.parametrize(
        "given,expected",
        [
            ("1m", 60),
            ("30m", 1800),
            ("1h", 3600),
            ("2d", 172800),
            ("45s", 45),
            (3600, 3600),
            ("", 0),
            (None, 0),
            ("nonsense", 0),
            (timedelta(minutes=5), 300),
            (timedelta(hours=2), 7200),
            # .seconds would give 0 here; only .total_seconds() is right past a day.
            (timedelta(days=2), 172800),
        ],
    )
    def test_step_seconds(self, given, expected):
        # Callers spell the step every way Prometheus accepts, because it used to travel
        # to the agent as an opaque string.
        assert _step_seconds(given) == expected


class TestValueFormatting:
    def test_integral_values_have_no_decimal_point(self):
        # ReplicasCount.get_data calls int() on this string directly, and int("1.0") raises.
        assert _format_value(1.0) == "1"
        assert _format_value(0.0) == "0"
        assert int(_format_value(3.0)) == 3

    def test_fractional_values_survive(self):
        assert float(_format_value(0.125)) == 0.125
        assert float(_format_value(1e-07)) == 1e-07

    def test_non_finite_does_not_raise(self):
        # int() raises on these. api-server sanitises them to null first, so this only
        # guards a provider that bypasses that -- but it must not take down the query.
        for value in (float("nan"), float("inf"), float("-inf")):
            assert isinstance(_format_value(value), str)


class TestResponseMapping:
    PAYLOAD = [{"metric": {"pod": "a"}, "timestamps": [100, 200, 300], "values": [1.0, 2.5, 3.0]}]

    def test_series_entries_are_the_flat_relay_shape(self):
        entries = _to_series_entries(self.PAYLOAD)
        assert entries == [{"metric": {"pod": "a"}, "timestamps": [100, 200, 300], "values": ["1", "2.5", "3"]}]

    def test_vector_entries_are_nested_and_take_the_latest_sample(self):
        entries = _to_vector_entries(self.PAYLOAD)
        assert entries == [{"metric": {"pod": "a"}, "value": {"timestamp": 300, "value": "3"}}]

    def test_null_samples_drop_with_their_timestamp(self):
        # NaN/+-Inf arrive as JSON null; dropping the value alone would shift every later
        # sample onto the wrong timestamp.
        payload = [
            {
                "metric": {"pod": "a"},
                "timestamps": [100, 200, 300],
                "values": [1.0, None, 3.0],
                "non_finite": {"nan": 1},
            }
        ]
        assert _to_series_entries(payload) == [{"metric": {"pod": "a"}, "timestamps": [100, 300], "values": ["1", "3"]}]

    def test_series_with_no_usable_samples_is_dropped(self):
        payload = [{"metric": {"pod": "a"}, "timestamps": [100], "values": [None]}]
        assert _to_series_entries(payload) == []
        assert _to_vector_entries(payload) == []

    def test_instant_flag_selects_the_vector_shape(self):
        body = {"results": [{"query_key": "q0", "payload": self.PAYLOAD}]}
        result, _ = _call(_response(body=body), instant=True)
        assert result == [{"metric": {"pod": "a"}, "value": {"timestamp": 300, "value": "3"}}]

    def test_range_returns_series_shape(self):
        body = {"results": [{"query_key": "q0", "payload": self.PAYLOAD}]}
        result, _ = _call(_response(body=body))
        assert result[0]["timestamps"] == [100, 200, 300]


class TestErrorMapping:
    def test_empty_results_is_no_data_not_a_failure(self):
        result, _ = _call(_response(body={"results": []}))
        assert result == []

    def test_per_query_error_raises(self):
        # api-server answers 200 with a per-result error when one query fails.
        body = {"results": [{"query_key": "q0", "payload": [], "error": "parse error at char 3"}]}
        with pytest.raises(PromQLQueryError, match="parse error"):
            _call(_response(body=body))

    def test_agent_not_connected_inside_a_200_is_its_own_type(self):
        body = {"results": [{"query_key": "q0", "payload": [], "error": "agent not connected"}]}
        with pytest.raises(AgentNotConnectedError):
            _call(_response(body=body))

    def test_agent_not_connected_as_a_400_message(self):
        # The relay's 503 reaches us as a 400 carrying a JSON message once it has been
        # through api-server's action error envelope.
        response = _response(status=400, body={"message": "relay: agent not connected for account"})
        with pytest.raises(AgentNotConnectedError):
            _call(response)

    def test_other_non_200_raises_the_generic_error(self):
        response = _response(status=500, body={"message": "boom"})
        with pytest.raises(ApiServerMetricsError, match="boom"):
            _call(response)

    def test_transport_failure_is_wrapped_with_the_query(self):
        session = MagicMock()
        session.request.side_effect = OSError("connection refused")
        with patch("server.metrics.api_server_metrics.get_http_session_with_retry", return_value=session):
            with pytest.raises(ApiServerMetricsError, match="connection refused"):
                query_metrics_via_api_server(account_id="a", tenant_id="t", query="up", start_time=START, end_time=END)
