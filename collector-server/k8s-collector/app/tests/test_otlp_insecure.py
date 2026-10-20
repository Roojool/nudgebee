"""TLS selection for the gRPC OTLP metric exporter.

Every chart writes OTEL_EXPORTER_OTLP_ENDPOINT without a scheme
(`otel-...svc:4317`) and the receiver behind it is plaintext. The OTel Python
SDK only applies the http/https convention when a scheme is present and
otherwise defaults to TLS, so a scheme-less endpoint produced a TLS channel
against a plaintext listener: every export failed with SSL_ERROR_SSL /
UNAVAILABLE and the collector's whole metric surface never left the pod. The
failure is invisible apart from a log line, so it needs a test.
"""

import os

import pytest

from metrics.otel_metrics import otlp_insecure

ENDPOINT_VARS = (
    "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
    "OTEL_EXPORTER_OTLP_INSECURE",
    "OTEL_EXPORTER_OTLP_METRICS_INSECURE",
)


@pytest.fixture(autouse=True)
def _clear_otlp_env(monkeypatch):
    for var in ENDPOINT_VARS:
        monkeypatch.delenv(var, raising=False)


@pytest.mark.parametrize(
    "endpoint,expected",
    [
        # The shipped value: no scheme, plaintext receiver.
        ("otel-deployment-collector-collector.otel.svc:4317", True),
        ("http://otel-deployment-collector-collector.otel.svc:4317", True),
        ("https://otlp.example.com:4317", False),
        ("HTTPS://otlp.example.com:4317", False),
        ("  https://otlp.example.com:4317  ", False),
        # Unset endpoint still must not silently opt into TLS.
        ("", True),
    ],
)
def test_insecure_follows_endpoint_scheme(monkeypatch, endpoint, expected):
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
    assert otlp_insecure() is expected


def test_metrics_endpoint_overrides_generic_endpoint(monkeypatch):
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://generic:4317")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "plaintext:4317")
    assert otlp_insecure() is True


@pytest.mark.parametrize(
    "value,expected",
    [("true", True), ("TRUE", True), ("1", True), ("false", False), ("0", False)],
)
def test_explicit_insecure_env_wins_over_scheme(monkeypatch, value, expected):
    # An https:// endpoint would otherwise mean secure; the operator's explicit
    # flag is the authority either way.
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otlp.example.com:4317")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_INSECURE", value)
    assert otlp_insecure() is expected


def test_metrics_specific_insecure_wins_over_generic(monkeypatch):
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_INSECURE", "false")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_METRICS_INSECURE", "true")
    assert otlp_insecure() is True


@pytest.mark.parametrize("blank", ["", "   "])
def test_present_but_empty_insecure_is_treated_as_unset(monkeypatch, blank):
    """Helm renders an unset value as an empty string.

    Reading that as "false" would send the exporter back to TLS against the
    plaintext receiver -- the exact failure this function exists to prevent --
    for anyone whose chart templates the var without a value.
    """
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector.otel.svc:4317")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_INSECURE", blank)
    assert otlp_insecure() is True


def test_empty_metrics_insecure_falls_through_to_generic(monkeypatch):
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otlp.example.com:4317")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_METRICS_INSECURE", "")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_INSECURE", "false")
    assert otlp_insecure() is False


def test_blank_insecure_still_respects_https_endpoint(monkeypatch):
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otlp.example.com:4317")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_INSECURE", "")
    assert otlp_insecure() is False


def test_shipped_dev_config_would_have_been_secure_before(monkeypatch):
    """Regression anchor: the exact env the dev collector runs with."""
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-deployment-collector-collector.otel.svc:4317")
    assert "://" not in os.environ["OTEL_EXPORTER_OTLP_ENDPOINT"]
    assert otlp_insecure() is True
