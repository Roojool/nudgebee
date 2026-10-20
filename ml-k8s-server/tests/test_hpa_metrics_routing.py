"""Which store answers each column of the prediction input.

The frame has six columns and they do not all come from the same place. Three name a
metric every provider has, so the account's default answers them. Two read eBPF
counters that only NudgeBee's node-agent writes, so they stay on Prometheus whatever
the account's default is. Getting that split wrong is silent: a Datadog account would
either lose three columns it could have had, or be asked for two it can never have.
"""

from datetime import datetime, timezone
from unittest.mock import patch

import pytest

from server.metrics.prometheus_metrics import _entries_for, _scaled
from server.metrics.prometheus_queries import (
    CpuUsage,
    MemUsage,
    ReplicasCount,
    RequestCount,
    RequestDuration,
)

START = datetime(2026, 9, 11, 10, 0, 0, tzinfo=timezone.utc)
END = datetime(2026, 9, 11, 11, 0, 0, tzinfo=timezone.utc)


def _call(query_obj, query="promql-here", instant=False):
    """Run one column and report which transport it used, with what."""
    with patch("server.metrics.prometheus_metrics.query_utilisation_via_api_server") as util, patch(
        "server.metrics.prometheus_metrics.query_metrics_via_api_server"
    ) as promql:
        util.return_value = {
            getattr(query_obj, "utilisation_key", None)
            or "x": [{"metric": {}, "timestamps": [1], "values": ["1048576"]}]
        }
        promql.return_value = [{"metric": {}, "timestamps": [1], "values": ["7"]}]
        result = _entries_for(
            query_obj=query_obj,
            query=query,
            account_id="acct-1",
            tenant_id="tenant-1",
            start_time=START,
            end_time=END,
            step="1h",
            instant=instant,
        )
    return result, util, promql


@pytest.mark.parametrize(
    "column, expected_key",
    [(ReplicasCount, "replica_defined"), (MemUsage, "memory_usage"), (CpuUsage, "cpu_usage")],
)
def test_a_column_every_provider_has_is_asked_for_by_name(column, expected_key):
    obj = column(namespace="prod", deployment_name="api")
    _, util, promql = _call(obj)

    util.assert_called_once()
    promql.assert_not_called()
    request = util.call_args.kwargs["request"]
    assert request["metrics"] == [expected_key]
    assert request["workload_namespace"] == "prod"
    assert request["workload_name"] == "api"
    assert request["kind"] == "deployment"
    # No aggregate: the frame is a time series per hour, not one collapsed point.
    assert "aggregate" not in util.call_args.kwargs


@pytest.mark.parametrize(
    "column, expected_key",
    [(RequestCount, "workload_http_request_rate"), (RequestDuration, "workload_http_latency_mean")],
)
def test_the_http_columns_are_asked_for_by_name_too(column, expected_key):
    """These read eBPF counters on Prometheus and APM traces on Datadog. Which of those
    happens is the layer's decision, not this file's — that is what lets a provider
    that gains the metrics later be implemented in one place."""
    obj = column(namespace="prod", deployment_name="api")
    _, util, promql = _call(obj)

    util.assert_called_once()
    promql.assert_not_called()
    assert util.call_args.kwargs["request"]["metrics"] == [expected_key]


def test_no_column_writes_its_own_probe_exclusion_any_more():
    """The probe-path list is part of what the key means and lives in api-server. A
    consumer re-adding its own would give the same key two meanings by account."""
    import server.metrics.prometheus_queries as q

    assert not hasattr(q, "PROMETHEUS_URL"), "dead constant"
    assert not hasattr(q, "QUERY"), "dead constant"
    for column in (RequestCount, RequestDuration, ReplicasCount, MemUsage, CpuUsage):
        assert column.utilisation_key, f"{column.__name__} should name a key"


def test_a_bare_query_string_still_goes_to_promql():
    """Callers that pass raw PromQL — the duration probes — are unaffected."""
    _, util, promql = _call(query_obj="up", query="up")
    promql.assert_called_once()
    util.assert_not_called()


def test_an_instant_query_stays_on_promql():
    """`instant` is used for one-off probes, not for these columns; the utilisation
    path has no instant form, so routing one there would silently change the shape."""
    _, util, promql = _call(MemUsage(namespace="prod", deployment_name="api"), instant=True)
    promql.assert_called_once()
    util.assert_not_called()


def test_memory_arrives_in_bytes_and_leaves_in_mebibytes():
    """Every utilisation key answers in bytes. This column has always been MiB, and the
    forecaster's thresholds are calibrated on that scale."""
    entries, _, _ = _call(MemUsage(namespace="prod", deployment_name="api"))
    assert float(entries[0]["values"][0]) == pytest.approx(1.0), "1048576 bytes is 1 MiB"


def test_cpu_and_replicas_are_not_rescaled():
    for column in (CpuUsage, ReplicasCount):
        entries, _, _ = _call(column(namespace="prod", deployment_name="api"))
        assert float(entries[0]["values"][0]) == pytest.approx(1048576.0), "no scale applied"


def test_an_unreadable_sample_is_passed_through_rather_than_zeroed():
    """The hourly grid reads whatever arrives as that bucket's value. Inventing a zero
    for a sample we could not parse would read as a measured idle hour."""
    assert _scaled("not-a-number", 0.5) == "not-a-number"
    assert _scaled(None, 0.5) is None
    assert float(_scaled("8", 0.5)) == pytest.approx(4.0)


def test_a_missing_agent_costs_one_column_not_the_whole_frame():
    """Losing the agent must not discard the columns the account's own provider answered.

    `fetch_metrics` used to catch AgentNotConnectedError and `return []` for EVERY
    column. That was right when all six needed the agent. It is wrong now: three of
    them resolve through the account's default provider, and throwing those away
    because the two eBPF columns could not be fetched loses exactly the data this
    change exists to get. The hourly grid zero-fills the missing one.
    """
    from datetime import datetime, timedelta, timezone
    from unittest.mock import patch

    from server.metrics.api_server_metrics import AgentNotConnectedError
    from server.metrics.prometheus_metrics import fetch_metrics

    end = datetime(2026, 9, 11, 12, 0, 0, tzinfo=timezone.utc)
    start = end - timedelta(hours=2)

    def answer(query_obj, query, account_id, tenant_id, start_time, end_time, step, instant):
        # The eBPF column is the one that needs the agent.
        if getattr(query_obj, "utilisation_key", None) in (
            "workload_http_request_rate",
            "workload_http_latency_mean",
        ):
            raise AgentNotConnectedError("agent not connected", 400)
        return [{"metric": {}, "timestamps": [int(start.timestamp())], "values": ["7"]}]

    with patch("server.metrics.prometheus_metrics._entries_for", side_effect=answer), patch(
        "server.metrics.prometheus_metrics.resolve_tenant_id", return_value="tenant-1"
    ):
        results = fetch_metrics(
            queries_obj=[
                MemUsage(namespace="prod", deployment_name="api"),
                RequestCount(namespace="prod", deployment_name="api"),
            ],
            start_time=start,
            end_time=end,
            duration=None,
            step="1h",
            account_id="acct-1",
            tenant_id="tenant-1",
        )

    assert results, "the column the provider answered survives the agent being down"


def test_the_hpa_walk_still_fails_loudly():
    """The forecast path keeps its own contract: it records a prediction, so it must
    not record one built from nothing. Only the single-shot path degrades."""
    import inspect

    from server.metrics.prometheus_metrics_hpa import PrometheusMetricsHPA

    source = inspect.getsource(PrometheusMetricsHPA._fetch_window)
    assert (
        "except AgentNotConnectedError" not in source
    ), "the backward walk propagates; the caller turns it into a ValueError"
