# Kubernetes Collector App

Python service that runs inside a target Kubernetes cluster and ships **metrics + events** to the central Nudgebee platform. The Python app handles non-realtime aggregation (queries, schedules, summaries); the Go-based [relay-server](../relay-server/README.md) sits next to it and handles the realtime WebSocket pipe back to the cloud.

## What this service does

```
   Kubernetes API ─┐
                   │      ┌──────────────────────┐                ┌─────────────┐
   Prometheus  ────┼─────▶│ k8s-collector/app    │──── HTTP ────▶│  Nudgebee   │
                   │      │ (Python, Flask)      │                │  cloud API  │
   RabbitMQ ──────┘       │                       │──── publish ─▶│  RabbitMQ   │
                          └──────────────────────┘                └─────────────┘
```

Specifically it:

1. Queries the in-cluster Kubernetes API + Prometheus for workload state.
2. Consumes work items off RabbitMQ (scheduled collection jobs, ad-hoc requests).
3. Posts aggregated payloads back to the cloud (`SERVICE_API_SERVER_URL`).
4. Exposes a `/health` HTTP endpoint for liveness/readiness probes.
5. Pushes its own operational metrics (RabbitMQ consumer health, handler timings)
   over OTLP -- see [Metrics](#metrics).

It does **not** handle pod-exec / log-streaming / WebSocket traffic — that's the relay-server's job.

## Prerequisites

- Python 3.11 or 3.12
- [Poetry](https://python-poetry.org/) (dependency management)
- A reachable Kubernetes API (in-cluster or via kubeconfig)
- A reachable Prometheus
- A reachable RabbitMQ (for non-server modes)

## Quickstart

```bash
cd collector-server/k8s-collector/app

# 1. Install dependencies
make install                     # = poetry install

# 2. Run locally (in-cluster auth requires a kubeconfig pointing at your dev cluster)
make run                         # = python app.py
```

## Collector modes

The same binary can run in three modes, selected by `COLLECTOR_MODE`:

| Mode      | What runs                                | When to use                                      |
|-----------|------------------------------------------|--------------------------------------------------|
| `both`    | HTTP server **and** RabbitMQ consumers   | Default. Suitable for single-replica deployments |
| `server`  | HTTP server only (no consumers)          | Horizontal scale: multiple HTTP-only replicas    |
| `worker`  | RabbitMQ consumers only (no HTTP)        | Horizontal scale: separate consumer replicas     |

## Configuration

Key environment variables (see code for a full list):

| Variable                      | Default              | Purpose                                           |
|-------------------------------|----------------------|---------------------------------------------------|
| `COLLECTOR_MODE`              | `both`               | Process role — `both` / `server` / `worker`        |
| `SERVICE_PORT`                | `5000`               | HTTP port (overridable per workload)               |
| `SERVICE_HOST`                | `0.0.0.0`            | HTTP bind address                                  |
| `ENV`                         | `PROD`               | Logging/behavior profile — `DEV` / `DEVELOPMENT` map to Debug; any other value (`PROD`, `PRODUCTION`, etc.) maps to Production |
| `APP_VERSION`                 | `0.0.0`              | Reported as `service.version` on exported telemetry |

Other expected env vars (provided by the chart):

- `RABBIT_MQ_HOST` / `_PORT` / `_USERNAME` / `_PASSWORD`
- `SERVICE_API_SERVER_URL` — upstream Nudgebee API endpoint
- `RELAY_SERVER_ENDPOINT` — for any cross-talk with the relay sidecar

## Module structure

```
apis/         # HTTP route handlers
controllers/  # Business logic invoked by APIs
config/       # Settings + environment resolution
db/           # DB clients
handlers/     # Long-running task handlers
metrics/      # OpenTelemetry instruments for this service's own metrics
middleware/   # Flask middleware
rabbitmq/     # Consumer wiring + message schemas
utils/        # Shared helpers
exception/    # Custom exceptions
```

Entry point is `app.py` — it selects which of the three modes (`both` / `server` / `worker`) to run via `COLLECTOR_MODE`.

## Metrics

The service records its own operational metrics (RabbitMQ consumer health, message
throughput, handler timings) as OpenTelemetry instruments in `metrics/` and
**pushes** them over OTLP. There is no `/metrics` scrape endpoint.

It previously exposed one for a Prometheus ServiceMonitor to scrape. That required
the customer's cluster to run the Prometheus Operator, and even where it did, the
samples stayed in the customer's Prometheus -- nothing forwarded them to Nudgebee,
so the metrics were effectively unreachable. Pushing removes both problems.

| Variable | Default | Purpose |
|---|---|---|
| `OTEL_EXPORTER` | *(unset)* | `otlp` pushes to the endpoint below; `console` prints to stdout; unset disables metric export |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | *(unset)* | Collector endpoint, e.g. `http://otel-collector:4317` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc` or `http/protobuf` |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60000` | Export interval in milliseconds |
| `OTEL_SERVICE_NAME` | `k8s-collector` | Reported as `service.name` |

Metric names and attribute keys are unchanged from the Prometheus version, so
dashboards and alerts keep working once a collector re-exports them.

### Checking metrics locally

No collector needed -- print them straight to your terminal:

```bash
OTEL_EXPORTER=console OTEL_METRIC_EXPORT_INTERVAL=10000 make run
```

For the full path over the wire, the root `docker-compose.yaml` runs an
`otel-collector` service (config in `deploy/otel/local-config.yaml`) that logs
what it receives and re-exposes it on `:8889` in Prometheus format:

```bash
docker compose up otel-collector k8s-collector-app
docker compose logs -f otel-collector   # what arrived
curl -s localhost:8889/metrics          # re-exported, for diffing
```

Metrics only move when messages flow. Publish a test message through the RabbitMQ
management UI (`localhost:15672`, guest/guest) to exchange `k8s_agent_exchange`
with routing key `k8s_agent_spend`; `docker restart` on the rabbitmq container
exercises the connection and consumer-health gauges.

## Development

```bash
make fmt           # black
make lint          # black --check + flake8 + mypy
make test          # pytest
make clean         # remove caches
```

Validation order before pushing: `make lint && make test`.

## Deployment

This service is deployed inside the target Kubernetes cluster (typically as a Deployment, not a DaemonSet — there's one collector per cluster, not per node). The Helm chart and image build are under `deploy/kubernetes/`. See the [root README](../../../README.md) for the broader install flow.
