"""Datadog and Elasticsearch rightsizing queries travel through api-server.

The queries themselves are unchanged — what changes is who runs them. api-server
resolves the account's integration and its credentials, so the Datadog keys and the
Elasticsearch password no longer travel in the trigger request (where they were logged
with it) and are not held here at all.

These lock the two things a unit test can still check: that each service asks
api-server for its own provider's dialect, and that the response it gets back is read
the same way as the provider's own reply.
"""

import asyncio
import json
from datetime import timedelta
from unittest.mock import patch

import pytest

from server.recommendation.vertical_rightsizing.models.allocations import ResourceAllocations
from server.recommendation.vertical_rightsizing.models.objects import K8sObjectData, PodData
from server.recommendation.vertical_rightsizing.services.metrics_datadog_service import (
    DatadogMetricsService,
    entries_from_datadog_response,
)
from server.recommendation.vertical_rightsizing.services.metrics_elasticsearch_service import (
    ElasticsearchMetricsService,
    entries_from_es_aggregations,
    pod_aggregates,
)


class _Strategy:
    def __init__(self, metrics):
        self.metrics = metrics


def _object():
    return K8sObjectData(
        cluster=None,
        name="app",
        container="app",
        namespace="ns",
        kind="Deployment",
        hpa=None,
        pods=[PodData(name="app-abc-1", deleted=False)],
        allocations=ResourceAllocations(requests={}, limits={}),
    )


class TestDatadogThroughApiServer:
    def test_the_query_goes_to_api_server_as_datadog_and_no_key_is_held(self):
        service = DatadogMetricsService(config=None, account_id="acct", executor=None, tenant_id="tenant")
        assert service.api_key == "", "the service is constructed without credentials"

        with patch(
            "server.recommendation.vertical_rightsizing.services.metrics_datadog_service."
            "query_metrics_via_api_server",
            return_value=[],
        ) as sent:
            service._query_datadog("avg:kubernetes.cpu.usage.total{kube_namespace:ns}", 1_700_000_000, 1_700_086_400)

        kwargs = sent.call_args.kwargs
        assert kwargs["metric_provider"] == "datadog"
        assert kwargs["query"].startswith("avg:kubernetes.cpu.usage.total")
        assert kwargs["account_id"] == "acct" and kwargs["tenant_id"] == "tenant"
        # The window travels as datetimes; api-server takes it from there.
        assert kwargs["start_time"].timestamp() == pytest.approx(1_700_000_000)
        assert kwargs["end_time"].timestamp() == pytest.approx(1_700_086_400)

    def test_both_transports_produce_the_same_series(self):
        """api-server's answer and Datadog's own answer read identically.

        The shared fixture: the left side is what Datadog returns, the right is what
        api-server returns for the same data. If either shape moves, this fails rather
        than the numbers quietly changing.
        """
        datadog_reply = {
            "series": [
                {
                    "scope": "pod_name:app-abc-1,kube_container_name:app",
                    "pointlist": [[1_700_000_000_000, 1_500_000.0], [1_700_000_060_000, None]],
                }
            ]
        }
        from_api_server = [
            {
                "metric": {"pod_name": "app-abc-1", "kube_container_name": "app"},
                "timestamps": [1_700_000_000],
                # api-server formats values as strings and drops the null, keeping the
                # arrays aligned.
                "values": ["1500000"],
            }
        ]
        assert entries_from_datadog_response(datadog_reply) == [
            {
                "metric": {"pod_name": "app-abc-1", "kube_container_name": "app"},
                "timestamps": [1_700_000_000.0],
                "values": [1_500_000.0],
            }
        ]

        service = DatadogMetricsService(config=None, account_id="acct", executor=None, tenant_id="t")
        direct = service._parse_series_by_pod_container(entries_from_datadog_response(datadog_reply))
        through_api_server = service._parse_series_by_pod_container(from_api_server)
        assert direct == through_api_server
        assert through_api_server["app"]["app-abc-1"] == [(1_700_000_000.0, 1_500_000.0)]


class TestElasticsearchThroughApiServer:
    def test_the_body_goes_to_api_server_as_a_dsl_query(self):
        service = ElasticsearchMetricsService(config=None, account_id="acct", executor=None, url="", tenant_id="tenant")
        assert service._transport is None, "no connection is built, so no password is held"

        with patch(
            "server.recommendation.vertical_rightsizing.services.metrics_elasticsearch_service."
            "query_metrics_via_api_server",
            return_value=[],
        ) as sent:
            asyncio.run(service.load_pods(_object(), timedelta(days=14)))

        kwargs = sent.call_args.kwargs
        assert kwargs["metric_provider"] == "ES"
        assert kwargs["request"] == {"query_type": "dsl", "batch": True}
        body = json.loads(kwargs["query"])
        assert body["aggs"]["pods"]["terms"]["field"] == "kubernetes.pod.name"
        # api-server ANDs its own range clause onto the body, so the window it is given
        # has to be the one the body already filters on.
        window = [f for f in body["query"]["bool"]["filter"] if "range" in f][0]["range"]["@timestamp"]
        assert kwargs["start_time"].isoformat() == window["gte"]
        assert kwargs["end_time"].isoformat() == window["lte"]

    def test_both_transports_produce_the_same_per_pod_values(self):
        """The shared fixture for Elasticsearch: one bucket, read both ways."""
        es_reply = {
            "aggregations": {
                "pods": {
                    "sum_other_doc_count": 0,
                    "buckets": [
                        {
                            "key": "app-abc-1",
                            "count": {"value": 120},
                            "last_seen": {"value": 1_787_811_900_000},
                            "pct": {"values": {"99.0": 2_500_000_000.0, "95.0": 1_000_000_000.0}},
                        }
                    ],
                }
            }
        }
        from_api_server = [
            {"metric": {"pods": "app-abc-1", "__name__": "count"}, "timestamps": [0], "values": ["120"]},
            {
                "metric": {"pods": "app-abc-1", "__name__": "last_seen"},
                "timestamps": [0],
                "values": ["1787811900000"],
            },
            {
                "metric": {"pods": "app-abc-1", "__name__": "pct", "statistic": "99.0"},
                "timestamps": [0],
                "values": ["2500000000"],
            },
            {
                "metric": {"pods": "app-abc-1", "__name__": "pct", "statistic": "95.0"},
                "timestamps": [0],
                "values": ["1000000000"],
            },
        ]

        direct = pod_aggregates(entries_from_es_aggregations(es_reply))
        through_api_server = pod_aggregates(from_api_server)
        assert direct == through_api_server

        agg = through_api_server["app-abc-1"]
        assert agg.percentile(99) == pytest.approx(2_500_000_000.0)
        # The caller asks with "95"; Elasticsearch keys it "95.0".
        assert agg.percentile(95) == pytest.approx(1_000_000_000.0)
        assert agg.count == pytest.approx(120)
        assert agg.timestamp == pytest.approx(1_787_811_900.0), "epoch millis become seconds"

    def test_a_pod_with_no_last_seen_is_stamped_rather_than_dropped(self):
        aggs = pod_aggregates(
            [{"metric": {"pods": "app-abc-1", "__name__": "count"}, "timestamps": [0], "values": ["5"]}]
        )
        assert aggs["app-abc-1"].timestamp > 0
