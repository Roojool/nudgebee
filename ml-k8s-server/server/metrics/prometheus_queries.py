import time
from datetime import UTC, datetime
from typing import Any, Dict, List, Optional

from pydantic import BaseModel
from server.utils.utils import MetricsServerConfigs
from server.utils.utils import generate_hourly_dict

# Define time parameters
END_TIME = int(time.time())  # Current timestamp
STEP = 3600  # Step size in seconds (1 hour) for Prometheus queries
BATCH_SIZE = 10  # Number of steps per batch for fetching metrics
TRAIN_BATCH_SIZE = 3600  # Number of steps per batch
RETRY_COUNT = 5  # Number of retries for failed requests


class MetricsInput(BaseModel):
    metric: Dict[str, Any]
    timestamps: List[int]
    values: List[str]

    def __init__(self, **data: Any):
        data["timestamps"] = list(map(int, data["timestamps"]))
        super().__init__(**data)


class PrometheusQuery:
    """One column of the prediction input, and how it is answered.

    Every column now names a ``utilisation_key``: it asks for the metric by name and
    api-server resolves the account's provider and builds the native query. Where a
    metric exists in only one place — the eBPF HTTP counters are produced by NudgeBee's
    node-agent — that is the layer's knowledge, not this file's, which is what lets a
    provider that gains them later be implemented in one place.

    A subclass without a key falls back to its own PromQL against Prometheus. Nothing
    here uses that now; it remains for callers that pass raw query strings.

    Columns land on a shared hourly grid (``get_data`` below), so one that cannot be
    answered is zero-filled rather than missing, and the frame still forms.
    """

    #: Utilisation key this column maps to, or None for "PromQL against Prometheus".
    utilisation_key: Optional[str] = None
    #: Factor from the key's unit to this column's unit (bytes -> MiB, say).
    utilisation_scale: float = 1.0

    def __init__(self, namespace, deployment_name) -> None:
        self.namespace = namespace
        self.deployment_name = deployment_name
        self.time_range = MetricsServerConfigs.time_range

    def get_data(
        self, data: List[MetricsInput], start_time: datetime, end_time: datetime, duration_in_days: int | None = None
    ):
        times: Dict[datetime, Any] = generate_hourly_dict(
            start_time=start_time, end_time=end_time, default_value=0, duration_in_days=duration_in_days
        )
        for dat in data:
            timestamps: List[int] = list(map(int, dat.timestamps))
            values = dat.values
            for _d in [(timestamps[i], values[i]) for i in range(min(len(timestamps), len(values)))]:
                times[datetime.fromtimestamp(_d[0], tz=UTC)] = _d[1]
        return [[i, times[i]] for i in times]

    def get_query(self) -> str:
        raise NotImplementedError


class ReplicasCount(PrometheusQuery):
    # The desired replica count (kube_deployment_spec_replicas and its per-kind
    # equivalents) rather than the ready one. They differ only while a rollout or a
    # scale event is in flight; on an hourly grid over 14 days that is a handful of
    # buckets, and desired is the better forecasting signal anyway — it is what the
    # autoscaler decided, not what had finished starting.
    utilisation_key = "replica_defined"

    def get_data(
        self, data: List[MetricsInput], start_time: datetime, end_time: datetime, duration_in_days: int | None = None
    ):
        times: Dict[datetime, Any] = generate_hourly_dict(
            start_time=start_time, end_time=end_time, default_value=0, duration_in_days=duration_in_days
        )
        for dat in data:
            timestamps: List[int] = list(map(int, dat.timestamps))
            values = dat.values
            for _d in [(timestamps[i], values[i]) for i in range(min(len(timestamps), len(values)))]:
                timestamp = datetime.fromtimestamp(_d[0], tz=UTC)
                times[timestamp] = times.get(timestamp, 0) + int(_d[1])
        return [[i, times[i]] for i in times]


class RequestCount(PrometheusQuery):
    # The key is answered from eBPF counters on Prometheus and from APM trace metrics
    # on Datadog, and it excludes probe paths in both — that exclusion is part of what
    # the key means, so this class no longer carries the list.
    #
    # It also fixes the column. The PromQL below averages container_http_requests_total
    # over the window, and that metric is a COUNTER: the result is the average
    # cumulative value, a ramp that resets when a pod restarts, not a rate. Measured on
    # a dev workload doing 17.5 requests/second, it reported 338,157.
    utilisation_key = "workload_http_request_rate"


class RequestDuration(PrometheusQuery):
    # Mean latency in seconds, the same sum/count this computes, with the denominator
    # guarded so an idle window is absent rather than +Inf.
    utilisation_key = "workload_http_latency_mean"


class MemUsage(PrometheusQuery):
    # The key answers in bytes, as every utilisation key does; this column is MiB.
    utilisation_key = "memory_usage"
    utilisation_scale = 1 / (1024 * 1024)


class CpuUsage(PrometheusQuery):
    utilisation_key = "cpu_usage"
