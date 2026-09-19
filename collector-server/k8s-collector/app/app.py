import json
import logging.config
import os
import signal
import sys
import time

from flask import Flask
from flask_restx import Api
from opentelemetry import trace
from opentelemetry.instrumentation.flask import FlaskInstrumentor
from opentelemetry.instrumentation.requests import RequestsInstrumentor
from opentelemetry.metrics import set_meter_provider
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import (
    ConsoleMetricExporter,
    PeriodicExportingMetricReader,
)
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider

from apis import register_urls
from metrics.otel_metrics import metric_views, otlp_insecure


class TraceIdFilter(logging.Filter):
    """Inject the active OTel trace_id / span_id (bare 32/16-hex, matching the Go
    services and Loki `trace_id` queries) into every LogRecord. Empty when no span
    is active. Kept here in app.py — rather than a separate top-level otel_setup
    module — so the logging.json filter reference resolves at import time and mypy
    doesn't see the file under two module names (app.otel_setup vs otel_setup)."""

    def filter(self, record: logging.LogRecord) -> bool:
        ctx = trace.get_current_span().get_span_context()
        record.trace_id = format(ctx.trace_id, "032x") if ctx.trace_id else ""
        record.span_id = format(ctx.span_id, "016x") if ctx.span_id else ""
        return True


def _build_metric_exporter(exporter_name: str):
    """Return the configured metric exporter, or None to leave metrics unexported.

    Mirrors relay-server/pkg/otel: "console" prints to stdout (local development,
    no collector required), "otlp" ships to OTEL_EXPORTER_OTLP_ENDPOINT over the
    protocol named by OTEL_EXPORTER_OTLP_PROTOCOL. Anything else -- including the
    unset default -- disables export, so an install that has not been given a
    collector endpoint behaves exactly as before rather than failing to start.
    """
    if exporter_name == "console":
        return ConsoleMetricExporter()
    if exporter_name == "otlp":
        protocol = os.environ.get("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
        if protocol.startswith("http"):
            from opentelemetry.exporter.otlp.proto.http.metric_exporter import (
                OTLPMetricExporter as HttpMetricExporter,
            )

            return HttpMetricExporter()
        if protocol == "grpc":
            from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import (
                OTLPMetricExporter as GrpcMetricExporter,
            )

            return GrpcMetricExporter(insecure=otlp_insecure())
        raise ValueError(f"Unsupported OTLP protocol: {protocol}")
    return None


def setup_telemetry() -> None:
    """Install real SDK providers for traces and metrics.

    Traces: a real TracerProvider is installed with no span processor, so spans are
    created but never exported -- deploy/otel/local-config.yaml's traces pipeline
    will not receive anything. It exists anyway because trace_id logging and inbound
    / consumer trace continuation need it: Python's no-op tracer does not preserve
    an extracted remote parent span context.

    Metrics: pushed over OTLP rather than scraped from a `/metrics` endpoint. The
    collector runs inside a customer cluster, where a pull model required a
    Prometheus Operator to be present and still left the samples in the
    customer's Prometheus with nothing forwarding them to Nudgebee.
    """
    # service.instance.id keeps the gunicorn workers apart. Scraping only ever hit
    # one worker, so duplicate series were invisible; with push, every worker
    # exports its own copy of the same metric and the backend would sum them into
    # nonsense (a connection-status gauge of 2, doubled message counters) unless
    # each process is a distinct instance.
    resource = Resource.create(
        {
            "service.name": os.environ.get("OTEL_SERVICE_NAME", "k8s-collector"),
            "service.version": os.environ.get("APP_VERSION", "0.0.0"),
            "service.instance.id": f"{os.environ.get('HOSTNAME', 'local')}-{os.getpid()}",
        }
    )
    trace.set_tracer_provider(TracerProvider(resource=resource))
    RequestsInstrumentor().instrument()

    exporter_name = os.environ.get("OTEL_EXPORTER", "").lower()
    metric_exporter = _build_metric_exporter(exporter_name)
    if metric_exporter is None:
        logging.info("OTel metrics export disabled (OTEL_EXPORTER=%r)", exporter_name)
        return

    # Parsed defensively: this runs at import, so a bad chart value would otherwise
    # be a CrashLoopBackOff rather than a degraded export interval.
    raw_interval = os.environ.get("OTEL_METRIC_EXPORT_INTERVAL", "60000")
    try:
        export_interval = int(raw_interval)
    except ValueError:
        logging.warning("Invalid OTEL_METRIC_EXPORT_INTERVAL=%r, falling back to 60000ms", raw_interval)
        export_interval = 60000

    reader = PeriodicExportingMetricReader(metric_exporter, export_interval_millis=export_interval)
    set_meter_provider(MeterProvider(resource=resource, metric_readers=[reader], views=metric_views()))
    logging.info("OTel metrics export enabled via %s exporter", exporter_name)


with open("config/logging.json", "rt") as f:
    logging_config = json.load(f)

logging.config.dictConfig(logging_config)

# Install the real SDK providers + outbound instrumentation after logging is
# configured. Required so trace_id logging and inbound/consumer trace continuation work.
setup_telemetry()

# Determine application environment - default to Production for security
ENV_MAPPING = {
    "DEV": "Debug",
    "DEVELOPMENT": "Debug",
    "PROD": "Production",
    "PRODUCTION": "Production",
}
APP_ENV = ENV_MAPPING.get(os.getenv("ENV", "PROD").upper(), "Production")

api = Api(doc="/" if APP_ENV == "Debug" else False)

DEFAULT_PORT = 5000
DEFAULT_HOST = "0.0.0.0"


class AppConfig:
    PORT = int(os.getenv("SERVICE_PORT", DEFAULT_PORT))
    HOST = os.getenv("SERVICE_HOST", DEFAULT_HOST)


class DevConfig(AppConfig):
    DEBUG = True
    TESTING = True


class ProdConfig(AppConfig):
    DEBUG = False
    TESTING = False


config_dict = {"Production": ProdConfig, "Debug": DevConfig}


def register_extensions(flask_app):
    api.init_app(flask_app)


def register_blueprints(flask_app) -> None:
    for route in register_urls.URL_CONFIG.urls:
        api.add_resource(route.view, route.url_prefix)


def create_app():
    flask_app = Flask(__name__)
    # Extract the inbound traceparent and start a server span so this agent
    # continues the caller's distributed trace instead of starting a new root.
    FlaskInstrumentor().instrument_app(flask_app)
    flask_app.config.from_object(config_dict[APP_ENV])
    register_extensions(flask_app)
    register_blueprints(flask_app)

    @flask_app.route("/health")
    def health():
        """Shallow process health check for Kubernetes probes."""
        return {"status": "ok"}

    return flask_app


def signal_handler_exit(sig, frame):
    logging.info("handler called with signal %s", sig)
    # Only reached when app.py is the entrypoint (local runs). Under gunicorn the
    # handler below is never installed and rabbitmq_client's interpreter-shutdown
    # hook does this instead.
    from rabbitmq.rabbitmq_client import stop_all_consumers

    stop_all_consumers()
    sys.exit(0)


def start_worker_mode():
    """Start worker mode - only consumes messages, no HTTP server"""
    logging.info("Starting worker mode - consuming messages only")

    # Register consumers explicitly
    from rabbitmq.rabbitmq_consumers import register_all_consumers

    register_all_consumers()

    # Keep worker process alive
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        logging.info("Worker mode shutting down")
        sys.exit(0)


def start_server_mode():
    """Start server mode - HTTP server only, no message consumption"""
    logging.info("Starting server mode - HTTP server only")

    flask_app = create_app()
    flask_app.run(
        host=flask_app.config.get("HOST", "0.0.0.0"),
        port=flask_app.config.get("PORT", 5000),
    )


def create_server_app():
    """Create app instance for server mode (used by gunicorn)"""
    return create_app()


def create_combined_app():
    """Create app instance with consumers for combined mode"""
    from rabbitmq.rabbitmq_consumers import register_all_consumers

    register_all_consumers()
    return create_app()


# Create the app instance for gunicorn based on COLLECTOR_MODE environment variable
collector_mode = os.getenv("COLLECTOR_MODE", "both")
if collector_mode == "server":
    app = create_server_app()  # Server mode - HTTP only
else:  # worker or both (default)
    app = create_combined_app()  # Combined mode with HTTP + message consumers

if __name__ == "__main__":
    signal.signal(signal.SIGINT, signal_handler_exit)
    signal.signal(signal.SIGTERM, signal_handler_exit)

    collector_mode = os.getenv("COLLECTOR_MODE", "both")
    if collector_mode == "server":
        # Server mode - HTTP only (no consumers)
        app = create_server_app()
        app.run(
            host=app.config.get("HOST", "0.0.0.0"),
            port=app.config.get("PORT", 5000),
        )
    elif collector_mode == "worker":
        start_worker_mode()
    else:  # both
        # Combined mode - HTTP + consumers
        app = create_combined_app()
        app.run(
            host=app.config.get("HOST", "0.0.0.0"),
            port=app.config.get("PORT", 5000),
        )
