"""Pins the windowed replica count that scales estimated savings.

The count at scan time swings with autoscaling, so the multiplier is the
time-weighted average of running pods over the observation window. The
denominator is the workload's observed lifetime inside the window: a
timestamp with no running pod carries no sample and is not averaged in.
"""

from datetime import timedelta

from server.recommendation.vertical_rightsizing.models.replicas import replica_stats_from_running_series

WINDOW = timedelta(hours=336)


def _series(pod, samples):
    return {"metric": {"pod": pod}, "values": [[ts, "1" if up else "0"] for ts, up in samples]}


def test_average_min_max_across_pods_per_timestamp():
    # Hour 1: two pods, hour 2: three pods, hour 3: one pod.
    series = [
        _series("web-a", [(1, True), (2, True), (3, True)]),
        _series("web-b", [(1, True), (2, True), (3, False)]),
        _series("web-c", [(2, True)]),
    ]
    stats = replica_stats_from_running_series(series, WINDOW)
    assert stats is not None
    assert stats.avg == (2 + 3 + 1) / 3
    assert stats.min == 1
    assert stats.max == 3
    assert stats.window_hours == 336


def test_hours_with_no_running_pod_are_not_averaged_in():
    # A workload that only existed for the last three of many hours averages
    # over those three, not over the whole window.
    series = [_series("web-a", [(100, True), (101, True), (102, True)]), _series("web-b", [(102, True)])]
    stats = replica_stats_from_running_series(series, WINDOW)
    assert stats is not None
    assert stats.avg == (1 + 1 + 2) / 3


def test_not_running_samples_count_as_absent():
    series = [_series("web-a", [(1, False), (2, False)])]
    assert replica_stats_from_running_series(series, WINDOW) is None


def test_empty_or_malformed_result_is_none():
    assert replica_stats_from_running_series([], WINDOW) is None
    assert replica_stats_from_running_series([{"metric": {}, "values": [["x", "y"], [1]]}], WINDOW) is None


def test_payload_is_rounded_and_carries_the_window():
    series = [_series("web-a", [(1, True), (2, True), (3, True)]), _series("web-b", [(1, True)])]
    stats = replica_stats_from_running_series(series, WINDOW)
    assert stats is not None
    assert stats.as_payload() == {"avg": 1.33, "min": 1, "max": 2, "window_hours": 336.0}
