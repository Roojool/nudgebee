"""PromQL transport that goes through api-server's metrics layer.

The relay only ever reaches the cluster agent's Prometheus. An account that connected
Prometheus directly, with no agent, therefore got nothing back from every metrics reader
in this service -- no rightsizing, no anomaly detection, no forecasting.

api-server's ``metrics_query`` action resolves the account's configured metrics provider
and picks the source: the connection the user configured (over HTTP, with the
integration's cluster labels applied to every selector) or the cluster agent's
Prometheus. Handing our PromQL to it is what PR #37924 did for the api-server-side
readers; this is the same move for ml-k8s-server. Part of #37930.

The mappers below return the relay's own wire shapes so that every existing consumer --
``MetricsInput``, the rightsizing loaders, the HPA batcher -- reads the result exactly as
it did before, and only the transport changes.
"""

import json
import logging
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional, Tuple, Union

from urllib3 import Retry

from server.utils.utils import ApiServerMetricsConfigs, get_http_session_with_retry, get_trace

from opentelemetry import context
from opentelemetry.propagate import inject

logger = logging.getLogger(__name__)

# PromQL only runs on a Prometheus-compatible engine. Naming the provider explicitly
# leaves api-server to pick the source, and stops a Datadog-default account from being
# handed PromQL it cannot run.
METRIC_PROVIDER_PROMETHEUS = "prometheus"

# The other providers whose own query language we send through this same call. The
# provider decides the dialect: PromQL for Prometheus, a Datadog metric query for
# Datadog, an Elasticsearch _search body for ES. api-server resolves the account's
# integration and its credentials, so none of them are held here.
METRIC_PROVIDER_DATADOG = "datadog"
METRIC_PROVIDER_ELASTICSEARCH = "ES"

_QUERY_KEY = "q0"

_SECONDS_PER_UNIT = {"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800}

# Retry the connect phase only. A refused connection means api-server never saw the
# query, so re-sending costs nothing -- which is not true of a read failure, where the
# query may already be running and a retry would double the cost of the slowest scans.
# Keeping read=0 preserves exactly what the previous ``retry_attempts=0`` achieved: that
# asked for no retries and got none, since POST is absent from urllib3's default
# allowed_methods. What it also got was no way back from a refused connection.
#
# services-server runs a single replica on many installs, so a restart is a ~30s window
# in which every query here is refused outright. connect=4 with backoff_factor=1 sleeps
# 0/2/4/8s: ~14s over five attempts, measured against a closed port. That stays inside
# api-server's own 30s budget for the anomaly call, so an answer that arrives after a
# retry is still consumed rather than abandoned. The connect allowance applies to POST:
# urllib3 checks allowed_methods for status retries, not for connect retries.
_CONNECT_RETRY = Retry(total=None, connect=4, read=0, status=0, other=0, redirect=0, backoff_factor=1)

# The connect phase gets its own short budget, separate from the read budget below. A
# refused connection fails instantly, so the retries above cost only their backoff -- but
# a connect that hangs instead of being refused (a stale kube-proxy DNAT rule pointing at
# an address that never answers the SYN) burns the whole timeout on every attempt. Sharing
# one 170s scalar across both phases would turn that into 5 x 170s + backoff, about 14
# minutes for a single query. Bounded here at 5 x 3s + 14s of backoff, which stays inside
# api-server's 30s budget for the anomaly call. In-cluster TCP connect is sub-millisecond,
# so 3s is already far more than a healthy connect needs.
_CONNECT_TIMEOUT_SECONDS = 3


class ApiServerMetricsError(Exception):
    """api-server could not answer the query at all."""

    def __init__(self, message: str, status: Optional[int] = None) -> None:
        super().__init__(message)
        self.message = message
        self.status = status


class AgentNotConnectedError(ApiServerMetricsError):
    """The account resolved to the cluster agent, and no agent is connected.

    Kept as its own type because callers have always treated this as "no data yet"
    rather than a failure -- see the equivalent branch in prometheus_metrics.py.
    """


class PromQLQueryError(ApiServerMetricsError):
    """The request succeeded but this individual query failed.

    api-server answers 200 with a per-result ``error`` when one query in a batch fails,
    so this is the normal shape of a bad query or an unreachable backend.
    """


def _step_seconds(step: Union[str, int, None]) -> int:
    """Step as the integer seconds api-server expects.

    Callers in this service spell the step every way Prometheus accepts -- "1h", "30m",
    "3600s", a bare int, a timedelta, or "" for "let the backend decide" -- because it
    used to travel to the agent as an opaque string.
    """
    if step is None or step == "":
        return 0
    # A timedelta stringifies to "0:01:00", which would parse to 0 and silently hand the
    # step choice to the backend.
    if hasattr(step, "total_seconds"):
        return int(step.total_seconds())
    if isinstance(step, int):
        return step
    text = str(step).strip().lower()
    if not text:
        return 0
    try:
        if text[-1] in _SECONDS_PER_UNIT:
            return int(float(text[:-1]) * _SECONDS_PER_UNIT[text[-1]])
        return int(float(text))
    except (ValueError, IndexError):
        logger.warning(f"Unrecognised step '{step}', letting the backend choose")
        return 0


def _epoch_millis(value: datetime) -> int:
    """Unix milliseconds, which is what FetchMetricsRequest takes (both sources /1000)."""
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    return int(value.timestamp() * 1000)


def _format_value(value: float) -> str:
    """Render a sample the way Prometheus does, as the string MetricsInput declares.

    Integral values must not come out as "1.0": ReplicasCount.get_data calls int() on
    the string directly, and int("1.0") raises.
    """
    try:
        if value == int(value) and abs(value) < 1e15:
            return str(int(value))
    except (ValueError, OverflowError):
        # int() raises on nan/inf. api-server sanitises those to null before they get
        # here, so this only guards against a provider that bypasses that.
        pass
    return repr(value)


def _samples(series: Dict[str, Any]) -> List[Tuple[int, str]]:
    """(timestamp, formatted value) pairs, dropping samples with no numeric value.

    A NaN/+-Inf sample is sent as JSON null with a count in ``non_finite``; the position
    is dropped along with its timestamp so the two arrays stay index-aligned.
    """
    timestamps = series.get("timestamps") or []
    values = series.get("values") or []
    pairs: List[Tuple[int, str]] = []
    for i in range(min(len(timestamps), len(values))):
        value = values[i]
        if value is None:
            continue
        pairs.append((int(timestamps[i]), _format_value(float(value))))
    non_finite = series.get("non_finite")
    if non_finite:
        logger.debug(f"Dropped non-finite samples from series: {non_finite}")
    return pairs


def _to_series_entries(payload: List[Dict[str, Any]]) -> List[dict]:
    """The relay's ``series_list_result`` shape: flat timestamps and values arrays."""
    entries = []
    for series in payload or []:
        pairs = _samples(series)
        if not pairs:
            continue
        entries.append(
            {
                "metric": series.get("metric") or {},
                "timestamps": [p[0] for p in pairs],
                "values": [p[1] for p in pairs],
            }
        )
    return entries


def _to_vector_entries(payload: List[Dict[str, Any]]) -> List[dict]:
    """The relay's ``vector_result`` shape: one nested {timestamp, value} per series."""
    entries = []
    for series in payload or []:
        pairs = _samples(series)
        if not pairs:
            continue
        timestamp, value = pairs[-1]
        entries.append(
            {
                "metric": series.get("metric") or {},
                "value": {"timestamp": timestamp, "value": value},
            }
        )
    return entries


def _error_message(response) -> str:
    try:
        body = response.json()
    except ValueError:
        return response.text or ""
    if isinstance(body, dict):
        return str(body.get("message") or body.get("error") or body)
    return str(body)


def query_metrics_via_api_server(
    account_id: str,
    tenant_id: str,
    query: str,
    start_time: datetime,
    end_time: datetime,
    step: Union[str, int, None] = None,
    instant: bool = False,
    metric_provider: str = METRIC_PROVIDER_PROMETHEUS,
    request: Optional[Dict[str, Any]] = None,
    timeout: Optional[int] = None,
) -> List[dict]:
    """Run one query through api-server and return it in the relay's wire shape.

    ``query`` is written in the provider's own language and ``metric_provider`` says
    which: PromQL by default, a Datadog metric query for METRIC_PROVIDER_DATADOG, a
    JSON _search body for METRIC_PROVIDER_ELASTICSEARCH (with
    ``request={"query_type": "dsl"}``). api-server holds the credentials.

    ``timeout`` bounds this call. A caller that abandons its own work sooner should
    pass its own budget: api-server keeps executing a request nobody is waiting for
    until its 180s cap, and rightsizing gives up on a workload after 45s.

    One query per call on purpose: ``queries`` is a map, but batching would put every
    query under api-server's single 180s handler budget and let one slow query fail the
    rest.

    Raises AgentNotConnectedError, PromQLQueryError or ApiServerMetricsError; callers
    decide which of those is fatal, matching what each entry point did before.
    """
    if not tenant_id:
        raise ApiServerMetricsError(f"no tenant for account {account_id}; cannot call api-server")

    with get_trace(__name__).start_as_current_span("query_metrics_via_api_server") as span:
        payload = {
            "action": {"name": "metrics_query"},
            "input": {
                "request": {
                    "account_id": str(account_id),
                    "metric_provider": metric_provider,
                    "queries": {_QUERY_KEY: query},
                    "start_time": _epoch_millis(start_time),
                    "end_time": _epoch_millis(end_time),
                    "step_interval": _step_seconds(step),
                    "instant": instant,
                    # These are background scans, not something a user typed.
                    "record_history": False,
                    **({"request": request} if request else {}),
                }
            },
        }

        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "X-ACTION-TOKEN": ApiServerMetricsConfigs.token,
            # Tenant with no user id gives a tenant-admin context in api-server, which is
            # what clears its per-account metrics read gate. Deliberately no x-user-id.
            "x-tenant-id": str(tenant_id),
        }
        current_context = context.get_current()
        if current_context:
            inject(headers, current_context)

        url = f"{ApiServerMetricsConfigs.url}/rpc/metrics"
        try:
            response = get_http_session_with_retry(retry_strategy=_CONNECT_RETRY).request(
                "POST",
                url,
                headers=headers,
                data=json.dumps(payload),
                timeout=(_CONNECT_TIMEOUT_SECONDS, timeout or ApiServerMetricsConfigs.timeout),
            )
        except Exception as e:
            raise ApiServerMetricsError(f"metrics_query request failed for query '{query}': {e}") from e

        if response.status_code != 200:
            message = _error_message(response)
            if "agent not connected" in message.lower() or response.status_code == 503:
                raise AgentNotConnectedError(message, response.status_code)
            raise ApiServerMetricsError(
                f"metrics_query failed with status {response.status_code}: {message}", response.status_code
            )

        try:
            body = response.json()
        except ValueError as e:
            raise ApiServerMetricsError(f"metrics_query returned a non-JSON body for query '{query}': {e}") from e

        results = (body or {}).get("results") or []
        if not results:
            span.set_attribute("series_count", 0)
            return []

        result = results[0]
        error = result.get("error")
        if error:
            # A per-query failure arrives as 200 + error, so this is the normal channel
            # for "agent not connected" too once the relay leg is the one that failed.
            if "agent not connected" in str(error).lower():
                raise AgentNotConnectedError(str(error), 200)
            raise PromQLQueryError(f"query '{query}' failed: {error}", 200)

        note = result.get("note")
        if note:
            # api-server states a capped or partial result in a note. Nothing downstream
            # reads it, so a truncated aggregation would otherwise look like the whole
            # answer — the same silent loss the note exists to end.
            logger.warning(f"api-server note for query '{query[:120]}': {note}")

        payload_series = result.get("payload") or []
        entries = _to_vector_entries(payload_series) if instant else _to_series_entries(payload_series)
        span.set_attribute("series_count", len(entries))
        return entries
