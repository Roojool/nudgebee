"""Savings contract for pod_right_sizing rows.

Requests are per pod, so a workload's saving is the per-pod delta times the
number of running pods; reporting the per-pod figure understated a 20-replica
Deployment twentyfold. Savings are floored at zero only once every container
has been merged, so a workload over-provisioned on one resource and
under-provisioned on another still reports its true net rather than an
inflated per-resource figure.
"""

import json

import pytest

from server.recommendation.vertical_rightsizing import (
    calculate_container_savings,
    finalize_workload_rows,
)

GB = 1024 * 1024 * 1024
CPU_RATE = 0.04  # $ per core-hour
MEM_RATE = 0.005  # $ per GB-hour
HOURS_PER_MONTH = 24 * 30


def _cpu_entry(allocated, recommended):
    return {"resource": "cpu", "allocated": {"request": allocated}, "recommended": {"request": recommended}}


def _memory_entry(allocated, recommended):
    return {"resource": "memory", "allocated": {"request": allocated}, "recommended": {"request": recommended}}


HALF_CORE_SAVED = 0.5 * CPU_RATE * HOURS_PER_MONTH


@pytest.mark.parametrize(
    "name,pods_count,expected",
    [
        ("single_pod", 1, HALF_CORE_SAVED),
        ("twenty_replicas", 20, HALF_CORE_SAVED * 20),
        ("three_replicas", 3, HALF_CORE_SAVED * 3),
        ("zero_pods_floors_at_one", 0, HALF_CORE_SAVED),
        ("negative_pods_floors_at_one", -5, HALF_CORE_SAVED),
    ],
)
def test_cpu_savings_scale_with_pod_count(name, pods_count, expected):
    content = [_cpu_entry(1.0, 0.5)]
    assert calculate_container_savings(content, CPU_RATE, MEM_RATE, pods_count) == pytest.approx(expected)


def test_pods_count_defaults_to_one():
    content = [_cpu_entry(1.0, 0.5)]
    assert calculate_container_savings(content, CPU_RATE, MEM_RATE) == pytest.approx(HALF_CORE_SAVED)


def test_memory_savings_scale_with_pod_count():
    content = [_memory_entry(4 * GB, 2 * GB)]
    expected = 2 * MEM_RATE * HOURS_PER_MONTH * 3
    assert calculate_container_savings(content, CPU_RATE, MEM_RATE, 3) == pytest.approx(expected)


def test_raising_requests_returns_a_negative_before_the_merge_clamp():
    content = [_cpu_entry(0.5, 1.0)]
    expected = -0.5 * CPU_RATE * HOURS_PER_MONTH * 4
    assert calculate_container_savings(content, CPU_RATE, MEM_RATE, 4) == pytest.approx(expected)


@pytest.mark.parametrize(
    "name,content",
    [
        ("not_enough_data", [{"resource": "cpu", "info": "Not enough data"}]),
        ("no_data", [{"resource": "cpu", "info": "No data"}]),
        ("unset_recommendation", [_cpu_entry(1.0, "?")]),
        ("missing_allocated", [{"resource": "cpu", "recommended": {"request": 0.5}}]),
    ],
)
def test_rows_without_usable_numbers_contribute_nothing(name, content):
    assert calculate_container_savings(content, CPU_RATE, MEM_RATE, 20) == 0.0


def _row(savings):
    payload = json.dumps({"app": [_cpu_entry(1.0, 0.5)]})
    return {"estimated_savings": savings, "recommendation": payload}


@pytest.mark.parametrize(
    "name,savings,expected",
    [
        ("positive_total_preserved", 120.0, 120.0),
        # A negative net is a real cost impact: applying the recommendation
        # raises the bill. The row keeps the figure so the Optimise table can
        # show it; savings roll-ups drop it at aggregation time instead.
        ("negative_total_preserved", -45.0, -45.0),
        ("zero_stays_zero", 0.0, 0.0),
    ],
)
def test_finalize_preserves_the_merged_savings_sign(name, savings, expected):
    rows = {"res-1": _row(savings)}
    # Priority 2 is OK rather than GOOD, so the row is rated, not dropped.
    finalize_workload_rows(rows, {"res-1": [2]})
    assert rows["res-1"]["estimated_savings"] == pytest.approx(expected)


def test_cost_increase_rows_still_take_the_no_change_drop():
    rows = {"keep": _row(-10.0), "drop": _row(-10.0)}
    # GOOD priority on every container marks a workload as needing no change.
    dropped = finalize_workload_rows(rows, {"keep": [2], "drop": [1]})
    assert dropped == 1
    assert "drop" not in rows
    assert rows["keep"]["estimated_savings"] == pytest.approx(-10.0)


# Golden vector shared with the k8s-collector twin (test_pod_rightsizing_savings.py):
# an app container that saves on both resources and a sidecar that pays on both.
# Per pod: app cpu 1.0->0.5 (+$14.40), app memory 4G->2G (+$7.20),
# sidecar cpu 0.1->0.3 (-$5.76), sidecar memory 1G->2G (-$3.60); net +$12.24.
MIXED_APP = [_cpu_entry(1.0, 0.5), _memory_entry(4 * GB, 2 * GB)]
MIXED_SIDECAR = [_cpu_entry(0.1, 0.3), _memory_entry(1 * GB, 2 * GB)]
MIXED_EXPECTED = {
    "app": {"cpu": 14.40, "memory": 7.20},
    "sidecar": {"cpu": -5.76, "memory": -3.60},
}


def test_each_entry_keeps_its_own_signed_figure():
    for name, content in (("app", MIXED_APP), ("sidecar", MIXED_SIDECAR)):
        total = calculate_container_savings(content, CPU_RATE, MEM_RATE, 1)
        by_resource = {e["resource"]: e["add_info"]["estimated_savings"] for e in content}
        assert by_resource == pytest.approx(MIXED_EXPECTED[name], abs=0.01)
        assert total == pytest.approx(sum(MIXED_EXPECTED[name].values()), abs=0.02)


def test_entry_figures_carry_the_replica_multiplier():
    content = [_cpu_entry(1.0, 0.5)]
    calculate_container_savings(content, CPU_RATE, MEM_RATE, 3)
    assert content[0]["add_info"]["estimated_savings"] == pytest.approx(3 * HALF_CORE_SAVED)


def test_unpriced_entries_get_no_figure():
    content = [{"resource": "memory", "info": "No data"}, _cpu_entry(1.0, 0.5)]
    calculate_container_savings(content, CPU_RATE, MEM_RATE, 1)
    assert "add_info" not in content[0]
    assert content[1]["add_info"]["estimated_savings"] == pytest.approx(HALF_CORE_SAVED)


def test_entry_figure_is_added_to_existing_add_info():
    entry = _cpu_entry(1.0, 0.5)
    entry["add_info"] = {"replicas": {"avg": 2}}
    calculate_container_savings([entry], CPU_RATE, MEM_RATE, 1)
    assert entry["add_info"]["replicas"] == {"avg": 2}
    assert entry["add_info"]["estimated_savings"] == pytest.approx(HALF_CORE_SAVED)
