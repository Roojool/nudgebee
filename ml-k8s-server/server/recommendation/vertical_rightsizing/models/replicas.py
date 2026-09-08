"""Replica count over the observation window, the multiplier behind estimated savings.

Requests are per pod, so a workload's monthly saving is the per-pod delta times
its replica count. The count at scan time swings with autoscaling — the same
recommendation on an HPA-scaled Deployment reported a different saving every
scan — so the multiplier is the time-weighted average over the same window the
recommendation itself is computed from, with min/max kept so the drawer can
show the range. The denominator is the workload's observed lifetime inside the
window, not the whole window: a one-day-old Deployment averages its real
replica count, not a fraction of it.

Keep the semantics in sync with the k8s-collector twin
(replica_stats_from_pod_rows in handlers/event_handler.py): that producer
derives the same figure from pod lifetimes in the metastore rather than from
Prometheus samples.
"""

from __future__ import annotations

from datetime import timedelta
from typing import Any, Dict, Iterable, Optional

import pydantic as pd


class ReplicaStats(pd.BaseModel):
    avg: float
    min: int
    max: int
    window_hours: float

    def as_payload(self) -> Dict[str, Any]:
        return {
            "avg": round(self.avg, 2),
            "min": self.min,
            "max": self.max,
            "window_hours": self.window_hours,
        }


def replica_stats_from_running_series(series: Iterable[Dict[str, Any]], window: timedelta) -> Optional[ReplicaStats]:
    """Fold per-pod Running-phase range series into replica stats.

    ``series`` is the Prometheus range-query result for
    ``kube_pod_status_phase{phase="Running"} == 1`` over the window: one entry
    per pod with ``values`` as ``[timestamp, "1"]`` pairs. Each timestamp's
    replica count is the number of pods with a sample there. Timestamps with
    no pod at all carry no sample and are therefore not part of the average —
    that is what makes the denominator the observed lifetime.
    """
    counts: Dict[float, int] = {}
    for entry in series:
        if not isinstance(entry, dict):
            continue
        values = entry.get("values")
        if not isinstance(values, (list, tuple)):
            continue
        for point in values:
            try:
                ts = float(point[0])
                running = float(point[1]) >= 1
            except (TypeError, ValueError, IndexError):
                continue
            if running:
                counts[ts] = counts.get(ts, 0) + 1
    if not counts:
        return None
    values = list(counts.values())
    return ReplicaStats(
        avg=sum(values) / len(values),
        min=min(values),
        max=max(values),
        window_hours=window.total_seconds() / 3600,
    )
