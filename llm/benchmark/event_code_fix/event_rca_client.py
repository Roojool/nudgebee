"""Drives one scenario through the real pipeline: inject a synthetic alert,
look up the event it produced, then run diagnosis+fix via /v1/analyze/event/rca.

No organic detection wait (see docs/code-analysis-eval-spec.md's revision
note) — the alert is posted directly through the real, already-supported
prometheus-alertmanager webhook, so turnaround is webhook-processing latency,
not a metric-evaluation cycle.
"""

from __future__ import annotations

import os
import sys
import time
from datetime import datetime, timezone
from typing import Any

import requests

TERMINAL_OK = {"completed"}
TERMINAL_BAD = {"failed", "terminated", "killed"}


def env(name: str, default: str | None = None) -> str:
    v = os.environ.get(name, default)
    if v is None:
        sys.exit(f"{name} must be set")
    return v


class IngestionTimeout(Exception):
    """The injected alert never produced a queryable event in time.

    Distinct from a diagnosis or fix failure — see the `ingestion_timeout`
    bucket in the spec. Expected to be rare; a long wait here means webhook
    processing is actually stuck, not "detection hasn't happened yet".
    """


class EventCodeFixClient:
    def __init__(
        self,
        account_id: str | None = None,
        tenant_id: str | None = None,
        user_id: str | None = None,
    ) -> None:
        # api-server: alert injection + event lookup
        self.api_url = env("API_SERVER_URL").rstrip("/")
        self.api_token = env("ACTION_API_SERVER_TOKEN")
        self.webhook_url = env("ALERTMANAGER_WEBHOOK_URL")  # full URL incl. ?token=...
        # llm-server: diagnosis + fix
        self.llm_url = env("LLM_SERVER_URL").rstrip("/")
        self.llm_token = env("LLM_SERVER_TOKEN")
        # The dashboard passes the run's selected account; the CLI falls back
        # to the BENCHMARK_* env vars.
        self.account_id = account_id or env("BENCHMARK_ACCOUNT_ID")
        self.tenant_id = tenant_id or env("BENCHMARK_TENANT_ID")
        self.user_id = user_id or os.environ.get("BENCHMARK_USER_ID", "")

    # --- stage 1: inject the alert -----------------------------------------

    def post_alert(self, scenario: dict[str, Any]) -> None:
        alert = scenario["alert"]
        now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        payload = {
            "alerts": [
                {
                    "status": "firing",
                    "labels": {
                        "alertname": alert["alertname"],
                        **alert.get("labels", {}),
                    },
                    "annotations": {
                        "summary": alert.get("summary", alert["alertname"]),
                        "description": alert["log_lines"],
                    },
                    "startsAt": now,
                    "endsAt": "",
                    "fingerprint": alert["fingerprint"],
                    "generatorURL": "",
                }
            ],
            "externalURL": "",
        }
        # Observed ~14s even for a trivially-rejected payload against a local
        # dev stack — RabbitMQ publish appears to fail (credential mismatch
        # between api-server's and llm-server's .env RABBIT_MQ_* values is a
        # likely cause locally) and ProcessStoredWebhook falls back to
        # synchronous processing, which the handler already tolerates. 30s
        # cut it close; 60s gives headroom without masking a real hang.
        resp = requests.post(self.webhook_url, json=payload, timeout=60)
        resp.raise_for_status()

    # --- stage 2: look up the event the alert produced ----------------------

    def wait_for_event(
        self, fingerprint: str, timeout_seconds: int = 90, poll_interval: int = 5
    ) -> str:
        """Poll events_v2 (via the dashboard query engine) for a row matching
        `fingerprint`, and return its id.

        Raises IngestionTimeout if none appears within budget — see the
        `ingestion_timeout` bucket in the spec: this is an infra/harness
        signal, not a diagnosis or fix result.

        Request/response shapes (EntityQueryRequest, QueryRequest,
        BinaryWhereClause) are verified against
        api-server/services/dashboard/model.go and services/query/entity_query.go.
        Confirmed live against a real local run (2026-09-18): the response is
        flat — `{"columns": [...], "rows": [...]}` — with no "data" wrapper.
        """
        deadline = time.monotonic() + timeout_seconds
        query = {
            "account_ids": [self.account_id],
            "datasource": "nudgebee",
            "query": {
                "table": "events_v2",
                "columns": [{"name": "id"}],
                "where": {"_binary": {"fingerprint": {"_eq": fingerprint}}},
                "limit": 1,
            },
        }
        while time.monotonic() < deadline:
            resp = requests.post(
                f"{self.api_url}/rpc/dashboard",
                json={
                    "action": {"name": "dashboards_execute_entity_query"},
                    "input": query,
                },
                headers={
                    "X-ACTION-TOKEN": self.api_token,
                    "x-tenant-id": self.tenant_id,
                },
                timeout=30,
            )
            resp.raise_for_status()
            resp_json = resp.json()
            rows = (resp_json.get("data") or resp_json).get("rows") or []
            if rows:
                return rows[0][0]
            time.sleep(poll_interval)
        raise IngestionTimeout(
            f"no event with fingerprint={fingerprint} within {timeout_seconds}s"
        )

    # --- stage 3: diagnosis + fix -------------------------------------------

    def run_rca(
        self, event_id: str, poll_interval: int = 10, timeout_seconds: int = 900
    ) -> dict[str, Any]:
        """POST /v1/analyze/event/rca and poll until terminal.

        Returns the final EventAnalysisResponse as a dict. Diagnosis
        (analysis/investigation/summary) and fix (git_diff/status_reason) both
        land in this same response when the fix stage ran — see the spec.
        """
        body = {
            "input": {
                "event_id": event_id,
                "account_id": self.account_id,
                "generate": True,
            }
        }
        headers = {"X-ACTION-TOKEN": self.llm_token, "x-tenant-id": self.tenant_id}

        deadline = time.monotonic() + timeout_seconds
        while time.monotonic() < deadline:
            resp = requests.post(
                f"{self.llm_url}/v1/analyze/event/rca",
                json=body,
                headers=headers,
                timeout=60,
            )
            resp.raise_for_status()
            data = (resp.json().get("data")) or resp.json()
            status = str(data.get("status", "")).lower()
            if status in TERMINAL_OK:
                return data
            if status in TERMINAL_BAD:
                return data
            time.sleep(poll_interval)
        return {"status": "TIMEOUT", "event_id": event_id}
