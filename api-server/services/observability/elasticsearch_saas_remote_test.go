package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Response bodies below follow what Elasticsearch 8.19 returns for a remote-cluster
// index expression (`remote:index`), captured against a cluster registered as its own
// remote. Names are generic.

// A remote cluster whose shards all rejected the query (here an unparseable date) is
// reported as skipped inside an HTTP 200 with zero hits.
const esSkippedRemoteSearch = `{
  "took": 3, "timed_out": false, "num_reduce_phases": 0,
  "_shards": {"total": 0, "successful": 0, "skipped": 0, "failed": 0},
  "_clusters": {"total": 1, "successful": 0, "skipped": 1, "running": 0, "partial": 0, "failed": 0,
    "details": {"remote": {"status": "skipped", "indices": "metrics-example", "timed_out": false,
      "failures": [{"shard": -1, "index": null, "reason": {
        "type": "search_phase_execution_exception", "reason": "all shards failed",
        "failed_shards": [{"shard": 0, "index": "remote:.ds-metrics-example-000001",
          "reason": {"type": "parse_exception", "reason": "failed to parse date field [[[Time:-24h]]]"}}]}}]}}},
  "hits": {"total": {"value": 0, "relation": "eq"}, "max_score": null, "hits": []}
}`

const esFieldCapsRemote = `{
  "indices": ["remote:.ds-metrics-example-000001"],
  "fields": {
    "_id": {"_id": {"type": "_id", "metadata_field": true, "searchable": true, "aggregatable": false}},
    "_tier": {"keyword": {"type": "keyword", "metadata_field": true, "searchable": true, "aggregatable": true}},
    "_doc_count": {"integer": {"type": "integer", "metadata_field": true, "searchable": false, "aggregatable": false}},
    "@timestamp": {"date": {"type": "date", "metadata_field": false, "searchable": true, "aggregatable": true}},
    "metricset.dimensions": {"object": {"type": "object", "metadata_field": false, "searchable": false, "aggregatable": false}},
    "metricset.dimensions.DBClusterIdentifier": {"keyword": {"type": "keyword", "metadata_field": false, "searchable": true, "aggregatable": true}},
    "metricset.value.sum": {"float": {"type": "float", "metadata_field": false, "searchable": true, "aggregatable": true}},
    "service.name": {"text": {"type": "text", "metadata_field": false, "searchable": true, "aggregatable": false}},
    "service.name.keyword": {"keyword": {"type": "keyword", "metadata_field": false, "searchable": true, "aggregatable": true}},
    "mixed": {
      "long": {"type": "long", "metadata_field": false, "searchable": true, "aggregatable": true},
      "keyword": {"type": "keyword", "metadata_field": false, "searchable": true, "aggregatable": true}
    }
  }
}`

func TestESIndexIsRemote(t *testing.T) {
	for index, want := range map[string]bool{
		"metrics-*":                        false,
		"metrics-aws.cloudwatch_metrics-x": false,
		"remote:metrics-*":                 true,
		"*remote:*metrics*":                true,
		"metrics-*,remote:metrics-*":       true,
		"metrics-*, -remote:metrics-old":   true,
		"":                                 false,
	} {
		assert.Equal(t, want, esIndexIsRemote(index), index)
	}
}

func TestESRemoteClusterFailure(t *testing.T) {
	t.Run("skipped remote with no hits is an error naming the real cause", func(t *testing.T) {
		err := esRemoteClusterFailure([]byte(esSkippedRemoteSearch))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remote (skipped)")
		assert.Contains(t, err.Error(), "failed to parse date field",
			"the innermost shard reason must surface, not the generic 'all shards failed'")
	})

	t.Run("skipped remote without per-cluster detail still explains itself", func(t *testing.T) {
		body := `{"_clusters":{"total":1,"successful":0,"skipped":1,"failed":0},
		  "hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
		err := esRemoteClusterFailure([]byte(body))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "skipped: 1")
	})

	t.Run("skipped remote with hits from another cluster is partial data, not an error", func(t *testing.T) {
		body := `{"_clusters":{"total":2,"successful":1,"skipped":1,"failed":0,"details":{}},
		  "hits":{"total":{"value":42,"relation":"eq"},"hits":[]}}`
		assert.NoError(t, esRemoteClusterFailure([]byte(body)))
	})

	t.Run("successful remote search with no hits is a real empty result", func(t *testing.T) {
		body := `{"_clusters":{"total":1,"successful":1,"skipped":0,"failed":0},
		  "hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
		assert.NoError(t, esRemoteClusterFailure([]byte(body)))
	})

	t.Run("partially skipped field listing keeps the fields that did answer", func(t *testing.T) {
		body := `{"_clusters":{"total":2,"successful":1,"skipped":1,"failed":0,
		  "details":{"remote":{"status":"skipped","indices":"metrics-*"}}},
		  "indices":["local-index"],"fields":{"service.name":{"keyword":{"type":"keyword"}}}}`
		assert.NoError(t, esRemoteClusterFailure([]byte(body)), "no hit count means this is not a search result to judge")
	})

	t.Run("local search responses are untouched", func(t *testing.T) {
		body := `{"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":0},"hits":[]}}`
		assert.NoError(t, esRemoteClusterFailure([]byte(body)))
	})
}

// readResponse is shared by every search call site, so the check has to fire there.
func TestReadResponse_FailsOnSkippedRemoteCluster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(esSkippedRemoteSearch))
	}))
	defer srv.Close()

	resp, err := esRequest("POST", srv.URL+"/remote:metrics-example/_search", "{}", &ElasticsearchConfig{Url: srv.URL}) //nolint:bodyclose // readResponse closes it
	require.NoError(t, err)
	_, err = readResponse(resp, "metric query")
	require.Error(t, err, "HTTP 200 with a skipped remote and 0 hits must not read as 'no data'")
}

func TestParseESFieldCaps(t *testing.T) {
	fields, err := parseESFieldCaps([]byte(esFieldCapsRemote))
	require.NoError(t, err)

	types := map[string]any{}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		types[f.Field] = f.Attributes["type"]
		names = append(names, f.Field)
	}

	assert.Equal(t, "keyword", types["metricset.dimensions.DBClusterIdentifier"], "dimensions keep their type")
	assert.Equal(t, "float", types["metricset.value.sum"])
	assert.Equal(t, "keyword", types["service.name.keyword"], "multi-fields are listed like _mapping lists them")
	assert.Equal(t, "keyword", types["mixed"], "conflicting types resolve to the first in sorted order")

	for _, dropped := range []string{"_id", "_tier", "_doc_count", "metricset.dimensions"} {
		assert.NotContains(t, types, dropped, "metadata fields and object containers are not queryable leaves")
	}
	assert.IsIncreasing(t, names, "field list must be stably ordered")
}

func TestESFetchMetricFields_RemoteIndexUsesFieldCaps(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(esFieldCapsRemote))
	}))
	defer srv.Close()

	out, err := esFetchMetricFields(&ElasticsearchConfig{Url: srv.URL}, "remote:metrics-*")
	require.NoError(t, err)
	assert.Equal(t, "/remote:metrics-*/_field_caps", path, "_mapping cannot list a remote index")

	labels := map[string]bool{}
	for _, f := range out {
		labels[f.Label] = true
	}
	assert.True(t, labels["metricset.dimensions.DBClusterIdentifier"])
}

func TestESFetchMetricFields_EmptyIsAnError(t *testing.T) {
	// A wildcard `_mapping` on a remote expression, or a pattern matching nothing,
	// comes back as `{}` with HTTP 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	_, err := esFetchMetricFields(&ElasticsearchConfig{Url: srv.URL}, "metrics-none-*")
	require.Error(t, err, "an empty field list must not be returned as success")
	assert.Contains(t, err.Error(), "metrics-none-*")
}
