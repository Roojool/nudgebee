"""Batch sizing for the rightsizing Prometheus loaders.

A subquery re-evaluates its inner expression once per step across the whole
window, so a long window makes one query expensive in a way `pods_batch_size`
alone never accounted for. These cases pin the rule that shrinks the batch for
those queries and leaves every other loader alone.
"""

import datetime

from server.recommendation.vertical_rightsizing.metrics.base import PrometheusMetric
from server.recommendation.vertical_rightsizing.metrics.cpu import CPULoader, PercentileCPULoader


def _loader(cls) -> PrometheusMetric:
    return cls(prometheus=None, service_name="test")


def _batch_size(loader, period, step, object_=None):
    duration_str = loader._step_to_string(period)
    step_str = f"{round(step.total_seconds())}s"
    query = loader.get_query(object_ or _FakeObject(), duration_str, step_str)
    return loader._effective_batch_size(query, duration_str, step_str, period, step)


class _FakePod:
    def __init__(self, name: str) -> None:
        self.name = name


class _FakeObject:
    """The slice of K8sObjectData the query builders read."""

    def __init__(self, pod_count: int = 40) -> None:
        self.namespace = "lt000028"
        self.container = "srvr"
        self.pods = [_FakePod(f"pod-{i}") for i in range(pod_count)]


def test_long_window_percentile_query_shrinks_the_batch():
    # The shape that timed out in production: a 14d window at 75s resolution.
    loader = _loader(PercentileCPULoader(92))
    period = datetime.timedelta(days=14)
    step = datetime.timedelta(seconds=75)

    batch = _batch_size(loader, period, step)

    points = int(period.total_seconds() // step.total_seconds())
    assert batch * points <= PrometheusMetric.MAX_SUBQUERY_SAMPLES
    assert batch < loader.pods_batch_size, "a 14d:75s subquery must not keep the default batch"
    assert batch >= 1


def test_short_window_percentile_query_keeps_the_default_batch():
    # A 1d window at the same resolution is cheap enough to leave alone, so the
    # rule must not make every rightsizing run chattier than it is today.
    loader = _loader(PercentileCPULoader(92))

    batch = _batch_size(loader, datetime.timedelta(days=1), datetime.timedelta(seconds=75))

    assert batch == loader.pods_batch_size


def test_loader_without_a_subquery_is_untouched():
    # CPULoader has no [duration:step] subquery, so its cost scales with pods
    # only and the default batch still applies at any window length.
    loader = _loader(CPULoader)

    batch = _batch_size(loader, datetime.timedelta(days=14), datetime.timedelta(seconds=75))

    assert batch == loader.pods_batch_size


def test_batch_never_drops_below_one():
    # An extreme window must still produce a queryable batch rather than 0.
    loader = _loader(PercentileCPULoader(92))

    batch = _batch_size(loader, datetime.timedelta(days=365), datetime.timedelta(seconds=15))

    assert batch >= 1


def test_batching_is_semantically_neutral_for_the_percentile_query():
    # The rule is only safe because each subquery groups by pod, so a pod's
    # series does not depend on which batch it was queried in. Guard that the
    # grouping stays in the query.
    loader = _loader(PercentileCPULoader(92))
    query = loader.get_query(_FakeObject(), "14d", "75s")

    assert "by (container, pod, job)" in query
