"""Rightsizing by metric key, for every provider at once.

Two contracts are enforced here and neither is about a query. The first is the shape
the strategy reads — one row per pod, every declared metric present, no zeros invented
for a measurement that never happened. The second is that a scan survives: thousands
of workloads go through this, and one that cannot be answered must not end the run.
"""

import asyncio
from datetime import timedelta

import numpy as np
import pytest

from server.metrics.api_server_metrics import ApiServerMetricsError
from server.recommendation.vertical_rightsizing.models.allocations import ResourceAllocations
from server.recommendation.vertical_rightsizing.models.objects import K8sObjectData, PodData
from server.recommendation.vertical_rightsizing.services.metrics_utilisation_service import (
    CPU_USAGE_KEY,
    MEMORY_MAX_USAGE_KEY,
    MEMORY_WORKING_SET_KEY,
    OOMKILLED_LIMIT_KEY,
    UtilisationMetricsService,
    _pods_from_collapsed,
    _plan_requests,
)


class _Strategy:
    """Only `metrics` is read by gather_data."""

    def __init__(self, metrics):
        self.metrics = metrics


NUDGEBEE_METRIC_KEYS = [
    "cpu_percentile_99",
    "cpu_percentile_97",
    "cpu_percentile_95",
    "cpu_percentile_92",
    "MaxMemoryLoader",
    "MaxUsageMemoryLoader",
    "CPUAmountLoader",
    "MemoryAmountLoader",
    "MaxOOMKilledMemoryLoader",
]


def _object(pods=("app-abc-1", "app-abc-2"), container="app"):
    return K8sObjectData(
        cluster=None,
        name="app",
        container=container,
        namespace="ns",
        kind="Deployment",
        hpa=None,
        pods=[PodData(name=p, deleted=False) for p in pods],
        allocations=ResourceAllocations(requests={}, limits={}),
    )


def _service(**kwargs):
    defaults = dict(config=None, account_id="acct", executor=None, tenant_id="tenant")
    defaults.update(kwargs)
    return UtilisationMetricsService(**defaults)


def _series(pod, value, container="app", ts=1_700_000_000.0):
    return {"metric": {"pod": pod, "container": container}, "timestamps": [ts], "values": [value]}


def _capture(service, answers):
    """Record every request and answer from a map of aggregate -> {key: [series]}."""
    sent = []

    async def fake_fetch(object, keys, *, aggregate, start, end, pod_names, step=None):
        sent.append({"keys": list(keys), "aggregate": aggregate, "pod_names": pod_names, "step": step})
        return answers.get(aggregate, {})

    service._fetch = fake_fetch
    return sent


# --- the request plan ------------------------------------------------------------


def test_one_request_per_aggregate_not_one_per_loader():
    """`aggregate` collapses a whole request, so it is what forces the grouping.

    Four CPU percentiles cannot share a call. The two "max" memory keys can, and so
    can the two counts, which is what turns nine loaders into six calls.
    """
    plan = _plan_requests(NUDGEBEE_METRIC_KEYS)
    by_aggregate = dict(plan)

    assert len(plan) == 6
    assert by_aggregate["p99"] == [CPU_USAGE_KEY]
    assert by_aggregate["p92"] == [CPU_USAGE_KEY]
    assert by_aggregate["max"] == [MEMORY_WORKING_SET_KEY, MEMORY_MAX_USAGE_KEY, OOMKILLED_LIMIT_KEY]
    assert by_aggregate["count"] == [CPU_USAGE_KEY, MEMORY_WORKING_SET_KEY]


def test_the_percentile_comes_from_the_metric_name_not_a_table():
    """The strategy renders its settings into the key, so a retuned percentile needs
    no change on either side of the call."""
    assert dict(_plan_requests(["cpu_percentile_99.9"]))["p99.9"] == [CPU_USAGE_KEY]


def test_a_metric_nobody_maps_is_skipped_rather_than_guessed_at():
    assert _plan_requests(["SomeFutureLoader"]) == []


# --- the shape the strategy reads -------------------------------------------------


def test_every_declared_metric_comes_back_keyed_by_pod():
    service = _service()
    _capture(
        service,
        {
            "p99": {CPU_USAGE_KEY: [_series("app-abc-1", 0.42), _series("app-abc-2", 0.31)]},
            "p97": {CPU_USAGE_KEY: [_series("app-abc-1", 0.40)]},
            "p95": {CPU_USAGE_KEY: [_series("app-abc-1", 0.38)]},
            "p92": {CPU_USAGE_KEY: [_series("app-abc-1", 0.35)]},
            "max": {
                MEMORY_WORKING_SET_KEY: [_series("app-abc-1", 1.5e9)],
                MEMORY_MAX_USAGE_KEY: [_series("app-abc-1", 1.9e9)],
                OOMKILLED_LIMIT_KEY: [_series("app-abc-1", 2.0e9)],
            },
            "count": {
                CPU_USAGE_KEY: [_series("app-abc-1", 827)],
                MEMORY_WORKING_SET_KEY: [_series("app-abc-1", 812)],
            },
        },
    )

    result = asyncio.run(service.gather_data(_object(), _Strategy(NUDGEBEE_METRIC_KEYS), timedelta(days=14)))

    assert set(result) == set(NUDGEBEE_METRIC_KEYS)
    # values[0, 1] is what the strategy reads: one row, [timestamp, value].
    assert result["cpu_percentile_99"]["app-abc-1"].shape == (1, 2)
    assert result["cpu_percentile_99"]["app-abc-1"][0, 1] == pytest.approx(0.42)
    assert result["cpu_percentile_99"]["app-abc-2"][0, 1] == pytest.approx(0.31)
    assert result["MaxMemoryLoader"]["app-abc-1"][0, 1] == pytest.approx(1.5e9)
    assert result["MaxUsageMemoryLoader"]["app-abc-1"][0, 1] == pytest.approx(1.9e9)
    assert result["MaxOOMKilledMemoryLoader"]["app-abc-1"][0, 1] == pytest.approx(2.0e9)
    assert result["CPUAmountLoader"]["app-abc-1"][0, 1] == pytest.approx(827)


def test_the_two_memory_maxima_do_not_get_crossed():
    """One request carries three keys with the same aggregate. Reading them back by
    position rather than by key would silently size every limit off the working set.
    """
    service = _service()
    _capture(
        service,
        {
            "max": {
                MEMORY_WORKING_SET_KEY: [_series("app-abc-1", 100)],
                MEMORY_MAX_USAGE_KEY: [_series("app-abc-1", 999)],
            }
        },
    )
    result = asyncio.run(
        service.gather_data(_object(), _Strategy(["MaxMemoryLoader", "MaxUsageMemoryLoader"]), timedelta(days=14))
    )
    assert result["MaxMemoryLoader"]["app-abc-1"][0, 1] == pytest.approx(100)
    assert result["MaxUsageMemoryLoader"]["app-abc-1"][0, 1] == pytest.approx(999)


def test_a_key_the_provider_cannot_answer_is_empty_and_not_zero():
    """MaxUsageMemoryLoader is declared warning_on_no_data = False so the limit falls
    back to the request. A zero would read as a real measurement and floor the limit
    at nothing."""
    service = _service()
    _capture(service, {"max": {MEMORY_WORKING_SET_KEY: [_series("app-abc-1", 1.5e9)]}})

    result = asyncio.run(service.gather_data(_object(), _Strategy(NUDGEBEE_METRIC_KEYS), timedelta(days=14)))

    assert result["MaxUsageMemoryLoader"] == {}
    assert result["MaxOOMKilledMemoryLoader"] == {}
    assert set(result) == set(NUDGEBEE_METRIC_KEYS)


def test_only_the_requested_container_is_read():
    """The keys are per container. A sidecar filed under the pod name would be read as
    the application's own usage."""
    service = _service()
    _capture(
        service,
        {
            "max": {
                MEMORY_WORKING_SET_KEY: [
                    _series("app-abc-1", 1.5e9, container="app"),
                    _series("app-abc-1", 9.9e9, container="istio-proxy"),
                ]
            }
        },
    )
    result = asyncio.run(service.gather_data(_object(), _Strategy(["MaxMemoryLoader"]), timedelta(days=14)))
    assert result["MaxMemoryLoader"]["app-abc-1"][0, 1] == pytest.approx(1.5e9)


def test_a_pod_with_no_measurement_is_dropped_rather_than_carried_as_nan():
    """A gap is dropped by the transport, so this is the second line of defence. It is
    worth having: one NaN reaching np.max over the workload loses every other pod's
    answer with it, and the result still renders."""
    service = _service()
    _capture(
        service,
        {"max": {MEMORY_WORKING_SET_KEY: [_series("app-abc-1", float("nan")), _series("app-abc-2", 2.0e9)]}},
    )
    result = asyncio.run(service.gather_data(_object(), _Strategy(["MaxMemoryLoader"]), timedelta(days=14)))
    assert "app-abc-1" not in result["MaxMemoryLoader"]
    assert result["MaxMemoryLoader"]["app-abc-2"][0, 1] == pytest.approx(2.0e9)


# --- what reaches the call --------------------------------------------------------


def test_the_request_asks_for_the_workload_not_for_a_list_of_pods():
    """The request names the workload; enumerating its pods on top is the caller doing
    the layer's job, and it makes the request scale with how often the workload
    redeploys.

    That breaks both providers: on Prometheus the query exceeds -search.maxQueryLen and
    is rejected (#39137); on Datadog one series per named pod blows the point budget, so
    the rollup coarsens and per-pod coverage stops meaning anything.

    The step still travels, because `count` is expressed in steps.
    """
    service = _service()
    sent = _capture(service, {})

    asyncio.run(
        service.gather_data(
            _object(pods=("app-abc-1", "app-abc-2")),
            _Strategy(NUDGEBEE_METRIC_KEYS),
            timedelta(days=14),
            step=timedelta(seconds=75),
        )
    )

    assert len(sent) == 6
    for request in sent:
        assert request["pod_names"] is None, "the pod list must not be sent"
        assert request["step"] == timedelta(seconds=75)


def test_an_empty_pod_list_keeps_nothing_rather_than_everything():
    """The distinction has to survive the CALL SITE, not just the filter.

    `if object.pods` treats an empty list as falsy, which turns "the caller resolved
    the owner chain and found no pods" into "no set supplied" -- keeping every pod the
    workload prefix matched, including a sibling's. That is how a workload gets sized
    on its neighbour's pods.
    """
    service = _service()
    _capture(service, {"max": {MEMORY_WORKING_SET_KEY: [_series("sibling-worker-xyz", 9.9e9)]}})

    result = asyncio.run(service.gather_data(_object(pods=()), _Strategy(["MaxMemoryLoader"]), timedelta(days=14)))

    assert result["MaxMemoryLoader"] == {}


def test_a_sibling_workloads_pods_are_dropped_from_the_answer():
    """Workload scope returns a SUPERSET: the layer falls back to a name prefix, and
    `services-server` also matches `services-server-worker` pods. Verified on dev, 412
    returned where 337 are correct.

    The container label does not disambiguate them — the sibling runs a container of the
    same name — so the owner-chain pod set is what removes them.
    """
    series = [
        {
            "metric": {"pod": "services-server-abc-1", "container": "services-server"},
            "timestamps": [1],
            "values": [1.0],
        },
        {
            "metric": {"pod": "services-server-worker-xyz-9", "container": "services-server"},
            "timestamps": [1],
            "values": [99.0],
        },
    ]
    kept = _pods_from_collapsed(series, "services-server", {"services-server-abc-1"})
    assert set(kept) == {"services-server-abc-1"}, "the sibling's pod must not survive"


def test_no_resolved_pod_set_keeps_everything_but_an_empty_one_keeps_nothing():
    """None and an empty set are different answers.

    None means the caller resolved nothing and every returned pod is kept. EMPTY means
    the caller looked and found none — conflating them would size a workload on its
    neighbours' pods.
    """
    series = [{"metric": {"pod": "app-1", "container": "app"}, "timestamps": [1], "values": [1.0]}]
    assert set(_pods_from_collapsed(series, "app", None)) == {"app-1"}
    assert _pods_from_collapsed(series, "app", set()) == {}


def test_the_container_filter_still_runs_before_the_pod_filter():
    """Neither is sufficient alone: the sibling shares the container name, and a sidecar
    shares the pod name."""
    series = [
        {"metric": {"pod": "app-1", "container": "app"}, "timestamps": [1], "values": [1.0]},
        {"metric": {"pod": "app-1", "container": "istio-proxy"}, "timestamps": [1], "values": [50.0]},
    ]
    kept = _pods_from_collapsed(series, "app", {"app-1"})
    assert set(kept) == {"app-1"}
    assert kept["app-1"][0][1] == 1.0, "the sidecar's value must not be read as the app's"


def test_the_hint_is_forwarded_exactly_as_given():
    """Both fields or neither: api-server defaults an empty source to the agent, so a
    provider without one would pin an HTTP-connected Prometheus onto the agent path."""
    service = _service(metric_provider="prometheus", metric_provider_source="agent")
    assert service.metric_provider == "prometheus"
    assert service.metric_provider_source == "agent"

    plain = _service()
    assert plain.metric_provider == ""
    assert plain.metric_provider_source == ""


# --- a scan must survive ----------------------------------------------------------


def test_a_failed_request_is_empty_data_not_a_failed_scan():
    service = _service()

    async def boom(*args, **kwargs):
        raise ApiServerMetricsError("api-server said 500")

    service._fetch = boom

    result = asyncio.run(service.gather_data(_object(), _Strategy(NUDGEBEE_METRIC_KEYS), timedelta(days=14)))
    assert set(result) == set(NUDGEBEE_METRIC_KEYS)
    assert all(value == {} for value in result.values())


def test_an_unexpected_error_is_caught_too():
    """The layer is HTTP and JSON; the ways it can surprise this service are not all
    ApiServerMetricsError, and a scan covering thousands of workloads cannot end on
    one of them."""
    service = _service()

    async def boom(*args, **kwargs):
        raise ValueError("unreadable payload")

    service._fetch = boom

    result = asyncio.run(service.gather_data(_object(), _Strategy(NUDGEBEE_METRIC_KEYS), timedelta(days=14)))
    assert all(value == {} for value in result.values())


# --- which pods a workload had ----------------------------------------------------


def test_pod_liveness_comes_from_the_last_timestamp():
    service = _service()

    async def fake_fetch(object, keys, *, aggregate, start, end, pod_names, step=None):
        assert aggregate == "", "a collapsed answer has one timestamp and no liveness in it"
        import time

        now = time.time()
        return {
            MEMORY_WORKING_SET_KEY: [
                {"metric": {"pod": "live"}, "timestamps": [now - 30], "values": [1.0]},
                {"metric": {"pod": "gone"}, "timestamps": [now - 86400], "values": [1.0]},
            ]
        }

    service._fetch = fake_fetch
    pods = asyncio.run(service.load_pods(_object(), timedelta(days=14)))
    by_name = {p.name: p.deleted for p in pods}
    assert by_name == {"live": False, "gone": True}


def test_the_inventory_delegate_answers_when_one_was_supplied():
    """On a Prometheus-family account the existing kube_pod_owner walk attributes pod
    incarnations that no longer exist. This switch changes the numbers, not the
    population they are computed over."""

    class _Inventory:
        def __init__(self):
            self.asked = False

        async def load_pods(self, object, period):
            self.asked = True
            return [PodData(name="from-owner-chain", deleted=False)]

        async def load_replica_stats(self, object, period):
            return "stats"

    inventory = _Inventory()
    service = _service(inventory=inventory)

    pods = asyncio.run(service.load_pods(_object(), timedelta(days=14)))
    assert inventory.asked
    assert [p.name for p in pods] == ["from-owner-chain"]
    assert asyncio.run(service.load_replica_stats(_object(), timedelta(days=14))) == "stats"


def test_without_an_inventory_the_replica_window_falls_back_to_the_live_count():
    assert asyncio.run(_service().load_replica_stats(_object(), timedelta(days=14))) is None


def test_an_unreadable_sample_does_not_end_the_pod_scan():
    service = _service()

    async def fake_fetch(object, keys, *, aggregate, start, end, pod_names, step=None):
        import time

        return {
            MEMORY_WORKING_SET_KEY: [
                {"metric": {"pod": "ok"}, "timestamps": ["not-a-number", time.time()], "values": [1.0, 1.0]},
                {"metric": {}, "timestamps": [time.time()], "values": [1.0]},
            ]
        }

    service._fetch = fake_fetch
    pods = asyncio.run(service.load_pods(_object(), timedelta(days=14)))
    assert [p.name for p in pods] == ["ok"]


def test_numpy_rows_are_float64_so_the_strategy_can_index_them():
    service = _service()
    _capture(service, {"max": {MEMORY_WORKING_SET_KEY: [_series("app-abc-1", 1.5e9)]}})
    result = asyncio.run(service.gather_data(_object(), _Strategy(["MaxMemoryLoader"]), timedelta(days=14)))
    array = result["MaxMemoryLoader"]["app-abc-1"]
    assert array.dtype == np.float64
