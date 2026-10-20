package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// apmTraceTransactionDoc is a traces-apm* transaction document, shaped as Elasticsearch
// returns it: ECS nests in _source even though the field mapping is flat/dotted.
// Values taken from a live cluster (dev, APM Server 8.19.11).
func apmTraceTransactionDoc() map[string]any {
	return map[string]any{
		"@timestamp": "2026-10-08T08:10:20.329Z",
		"processor":  map[string]any{"event": "transaction"},
		"trace":      map[string]any{"id": "8fbdb47574b3169b3bffd0fa862b4005"},
		// On a transaction, span.id and transaction.id are the same value.
		"span":        map[string]any{"id": "5c9c6e30fa890342"},
		"transaction": map[string]any{"id": "5c9c6e30fa890342", "name": "GET /cart", "duration": map[string]any{"us": float64(6879)}},
		"parent":      map[string]any{"id": "7101bce8903b219e"},
		"service":     map[string]any{"name": "cart", "environment": "production"},
		"event":       map[string]any{"outcome": "success"},
		"kubernetes":  map[string]any{"pod": map[string]any{"name": "cart-7d9f8b-xk2lp"}, "namespace": "demo"},
		"url":         map[string]any{"full": "http://cart:8080/cart"},
		"http":        map[string]any{"response": map[string]any{"status_code": float64(200)}},
		"labels":      map[string]any{"tier": "frontend"},
	}
}

// apmTraceSpanDoc is a traces-apm* span document — the outgoing-call counterpart. Note it
// carries span.name and span.duration.us and NO transaction.* fields.
func apmTraceSpanDoc() map[string]any {
	return map[string]any{
		"@timestamp": "2026-10-08T08:10:20.330Z",
		"processor":  map[string]any{"event": "span"},
		"trace":      map[string]any{"id": "8fbdb47574b3169b3bffd0fa862b4005"},
		"span": map[string]any{
			"id": "ba52e037097a8b66", "name": "SELECT FROM orders",
			"type": "db", "subtype": "redis",
			"duration":    map[string]any{"us": float64(1600)},
			"destination": map[string]any{"service": map[string]any{"resource": "product-catalog:8080"}},
		},
		"parent":     map[string]any{"id": "5c9c6e30fa890342"},
		"service":    map[string]any{"name": "cart"},
		"event":      map[string]any{"outcome": "failure"},
		"kubernetes": map[string]any{"pod": map[string]any{"name": "cart-7d9f8b-xk2lp"}, "namespace": "demo"},
	}
}

// TestMapAPMHitToTrace_Transaction covers the served-request document.
func TestMapAPMHitToTrace_Transaction(t *testing.T) {
	tr := mapAPMHitToTrace(apmTraceTransactionDoc())

	assert.Equal(t, "8fbdb47574b3169b3bffd0fa862b4005", tr.TraceID)
	assert.Equal(t, "5c9c6e30fa890342", tr.SpanID)
	assert.Equal(t, "7101bce8903b219e", tr.ParentSpanID)
	assert.Equal(t, "GET /cart", tr.SpanName, "a transaction's name is transaction.name")
	assert.Equal(t, "Server", tr.SpanKind, "processor.event=transaction means a served request")
	assert.Equal(t, "cart", tr.ServiceName)
	assert.Equal(t, "Ok", tr.StatusCode, "event.outcome=success maps to Ok")
	assert.Equal(t, "demo", tr.WorkloadNamespace)
	assert.Equal(t, "http://cart:8080/cart", tr.Resource)
	assert.Equal(t, "200", tr.HTTPStatusCode)

	// The µs→ns conversion. A pass-through would report 6879ns (~7µs) for a 6.9ms
	// request, under-reporting every latency by 1000x.
	assert.Equal(t, int64(6_879_000), tr.DurationNs)

	// The pod is the only k8s identity APM records, so it is exposed as a resource
	// attribute while workload_name falls back to the service.
	assert.Equal(t, "cart", tr.WorkloadName)
	assert.Equal(t, "cart-7d9f8b-xk2lp", tr.ResourceAttributes["kubernetes.pod.name"])
	assert.Equal(t, "production", tr.ResourceAttributes["service.environment"])
	assert.Equal(t, "frontend", tr.SpanAttributes["tier"], "labels.* are the user attributes")

	// ISO-8601 passes through normalizeESTimestamp untouched (unlike the
	// OTel-native layout, where @timestamp is an epoch-millis string).
	assert.Equal(t, "2026-10-08T08:10:20.329Z", tr.Timestamp)
	assert.Equal(t, tr.Timestamp, tr.StartTime)
}

// TestMapAPMHitToTrace_Span covers the outgoing-call document, whose fields are
// disjoint from a transaction's — the reason the mapper coalesces instead of branching.
func TestMapAPMHitToTrace_Span(t *testing.T) {
	tr := mapAPMHitToTrace(apmTraceSpanDoc())

	assert.Equal(t, "ba52e037097a8b66", tr.SpanID, "span.id is populated on both event types")
	assert.Equal(t, "5c9c6e30fa890342", tr.ParentSpanID)
	assert.Equal(t, "SELECT FROM orders", tr.SpanName, "a span's name is span.name")
	assert.Equal(t, "Client", tr.SpanKind, "processor.event=span means work this service initiated")
	assert.Equal(t, int64(1_600_000), tr.DurationNs, "span.duration.us is also microseconds")
	assert.Equal(t, "Error", tr.StatusCode, "event.outcome=failure maps to Error")

	// The dependency target VERBATIM, ":port" included. It is reported exactly as
	// stored because the same field is the filter target, a group-by dimension and
	// what GetLabelValues returns — stripping it here would make a filter on the
	// displayed value match nothing.
	assert.Equal(t, "product-catalog:8080", tr.DestinationName)
	assert.Equal(t, "product-catalog:8080", tr.DestinationWorkload)

	// APM records no namespace for the callee; kubernetes.namespace on this document
	// is the CALLER's, so it must not be reported as the destination's.
	assert.Empty(t, tr.DestinationNamespace)
	assert.Equal(t, "demo", tr.WorkloadNamespace, "the caller's namespace still populates workload_namespace")

	// A span has no url.full, so the dependency resource stands in as the resource.
	assert.Equal(t, "product-catalog:8080", tr.Resource)
}

// TestMapAPMHitToTrace_ResourceIgnoresHTTPMethod pins that an HTTP verb is never
// reported as a resource, and that an unknown resource stays empty rather than being
// filled with the span's own name — which has its own column, and which the OTel
// reader also leaves out.
func TestMapAPMHitToTrace_ResourceIgnoresHTTPMethod(t *testing.T) {
	doc := apmTraceTransactionDoc()
	delete(doc, "url")
	doc["http"] = map[string]any{"request": map[string]any{"method": "GET"}}

	tr := mapAPMHitToTrace(doc)
	assert.NotEqual(t, "GET", tr.Resource, "an HTTP verb is not a resource")
	assert.Empty(t, tr.Resource)
	assert.Equal(t, "GET /cart", tr.SpanName, "the name is still reported, in its own field")
}

// TestScaleAPMGroupLatenciesToNs is the guard for the unit mismatch between this
// layout and the TraceGroupingValues contract. The grouping aggregations average
// transaction.duration.us (microseconds) and parseTraceGroupBucket writes the result
// straight into fields the other two ES readers fill from nanosecond fields, so
// without scaling every grouped latency reads 1000x too small — and a 1.5ms request
// shown as 1.5µs looks like a fast service rather than a bug.
func TestScaleAPMGroupLatenciesToNs(t *testing.T) {
	scaled := scaleAPMGroupLatenciesToNs([]TraceGroupingValues{
		{DurationNS: 1500, P95Latency: 2000, P99Latency: 3000, MaxLatency: 4000, Count: 7},
		{DurationNS: 0, P95Latency: 0, P99Latency: 0, MaxLatency: 0},
	})

	assert.Equal(t, int64(1_500_000), scaled[0].DurationNS)
	assert.Equal(t, int64(2_000_000), scaled[0].P95Latency)
	assert.Equal(t, int64(3_000_000), scaled[0].P99Latency)
	assert.Equal(t, int64(4_000_000), scaled[0].MaxLatency)
	assert.Equal(t, 7, scaled[0].Count, "non-latency fields are untouched")

	// Zero stays zero rather than becoming a misleading floor.
	assert.Equal(t, int64(0), scaled[1].DurationNS)
	assert.Equal(t, int64(0), scaled[1].MaxLatency)
}

// TestMapAPMHitToTrace_UnknownOutcome pins that a missing or "unknown" outcome is not
// reported as success. APM records request outcome, not an OTel status code, so there
// is no basis for claiming Ok.
func TestMapAPMHitToTrace_UnknownOutcome(t *testing.T) {
	doc := apmTraceSpanDoc()
	doc["event"] = map[string]any{"outcome": "unknown"}
	assert.Equal(t, "Unset", mapAPMHitToTrace(doc).StatusCode)

	delete(doc, "event")
	assert.Equal(t, "Unset", mapAPMHitToTrace(doc).StatusCode)
}

// TestMapAPMHitToTrace_FlatSource covers a shipper that flattens _source to literal
// dotted keys instead of nesting it.
func TestMapAPMHitToTrace_FlatSource(t *testing.T) {
	tr := mapAPMHitToTrace(map[string]any{
		"@timestamp":              "2026-10-08T08:10:20.329Z",
		"processor.event":         "transaction",
		"trace.id":                "abc123",
		"span.id":                 "def456",
		"service.name":            "flat-service",
		"transaction.name":        "GET /flat",
		"transaction.duration.us": float64(2500),
		"event.outcome":           "success",
	})

	assert.Equal(t, "abc123", tr.TraceID)
	assert.Equal(t, "def456", tr.SpanID)
	assert.Equal(t, "flat-service", tr.ServiceName)
	assert.Equal(t, "GET /flat", tr.SpanName)
	assert.Equal(t, int64(2_500_000), tr.DurationNs)
	assert.Equal(t, "Ok", tr.StatusCode)
}

func TestMapAPMHitToTrace_Empty(t *testing.T) {
	tr := mapAPMHitToTrace(map[string]any{})
	assert.Empty(t, tr.TraceID)
	assert.Empty(t, tr.SpanID)
	assert.Equal(t, int64(0), tr.DurationNs)
	assert.Equal(t, "Unset", tr.StatusCode)
	assert.Empty(t, tr.SpanKind, "an absent processor.event yields no kind rather than a guess")
}

// TestMapAPMHitToTrace_DestinationIsVerbatim pins that whatever APM Server stored as
// the dependency resource is what gets reported, in every shape it writes. The reader
// used to strip a ":port" suffix, which desynchronised the displayed value from the
// filter/group/label-values value derived from the same field.
func TestMapAPMHitToTrace_DestinationIsVerbatim(t *testing.T) {
	for _, resource := range []string{
		"product-catalog:8080", // a service dependency, host:port
		"astronomy-db:5432",
		"mysql",              // a backend type, bare
		"kafka/orders",       // a messaging topic
		"elasticsearch:http", // not host:port — the suffix is a scheme
		"api.stripe.com:443", // an external HTTP dependency
	} {
		doc := apmTraceSpanDoc()
		doc["span"].(map[string]any)["destination"] = map[string]any{
			"service": map[string]any{"resource": resource},
		}
		tr := mapAPMHitToTrace(doc)
		assert.Equal(t, resource, tr.DestinationName, "destination for %q", resource)
		assert.Equal(t, resource, tr.DestinationWorkload, "destination workload for %q", resource)
	}
}

func TestMapAPMOutcome(t *testing.T) {
	assert.Equal(t, "Ok", mapAPMOutcome("success"))
	assert.Equal(t, "Ok", mapAPMOutcome("SUCCESS"))
	assert.Equal(t, "Error", mapAPMOutcome("failure"))
	assert.Equal(t, "Unset", mapAPMOutcome("unknown"))
	assert.Equal(t, "Unset", mapAPMOutcome(""))
	assert.Equal(t, "Unset", mapAPMOutcome("bogus"))
}

func TestMapAPMSpanKind(t *testing.T) {
	assert.Equal(t, "Server", mapAPMSpanKind("transaction"))
	assert.Equal(t, "Client", mapAPMSpanKind("span"))
	assert.Equal(t, "", mapAPMSpanKind("error"))
	assert.Equal(t, "", mapAPMSpanKind(""))
}

// TestEsAPMTraceIndexFor pins the precedence and, importantly, the default: the shared
// esTraceIndexFor falls back to Data Prepper's index, which would match nothing on an
// APM cluster.
func TestEsAPMTraceIndexFor(t *testing.T) {
	assert.Equal(t, "picked-*", esAPMTraceIndexFor(&ElasticsearchConfig{TraceIndex: "cfg-*"}, "picked-*"))
	assert.Equal(t, "cfg-*", esAPMTraceIndexFor(&ElasticsearchConfig{TraceIndex: "cfg-*"}, ""))
	assert.Equal(t, esAPMTraceIndex, esAPMTraceIndexFor(&ElasticsearchConfig{}, ""))
	assert.Equal(t, "traces-apm*", esAPMTraceIndex, "APM Server's own default data stream")
}

// TestMapAPMHitToHeatmap checks the waterfall projection shares the mapper's unit
// handling — the heatmap is where a 1000x duration error would be most visible.
func TestMapAPMHitToHeatmap(t *testing.T) {
	hm := mapAPMHitToHeatmap(apmTraceTransactionDoc())
	assert.Equal(t, "8fbdb47574b3169b3bffd0fa862b4005", hm.TraceID)
	assert.Equal(t, "5c9c6e30fa890342", hm.SpanID)
	assert.Equal(t, "GET /cart", hm.SpanName)
	assert.Equal(t, "cart", hm.ServiceName)
	assert.Equal(t, int64(6_879_000), hm.DurationNs)
	assert.Equal(t, "Ok", hm.StatusCode)

	hmSpan := mapAPMHitToHeatmap(apmTraceSpanDoc())
	assert.Equal(t, "SELECT FROM orders", hmSpan.SpanName, "span.name is used when transaction.name is absent")
	assert.Equal(t, int64(1_600_000), hmSpan.DurationNs)
}

// elasticAPMUnsupportedCanonicalFields are canonical trace fields the Elastic APM
// layout genuinely cannot answer. Listing them explicitly — rather than pointing the
// mapping at a field that merely looks right — is what keeps the gap honest.
var elasticAPMUnsupportedCanonicalFields = map[string]string{
	// APM stores no namespace for the CALLEE. kubernetes.namespace on these documents
	// is the caller's own, so mapping it here would make a filter on "the
	// destination's namespace" silently match the caller's.
	"destination_workload_namespace": "Elastic APM records no callee namespace",
}

// TestElasticAPMTraceLabelMappingCoversCanonicalFields ties the mapping to the
// canonical vocabulary. providerDeclaresTraceFields advertises canonical fields based
// on the mapping being non-empty, and the Traces tab renders a fixed column set — an
// unmapped canonical field renders blank rather than erroring, so each omission has to
// be a decision rather than an oversight.
func TestElasticAPMTraceLabelMappingCoversCanonicalFields(t *testing.T) {
	for _, field := range allCanonicalTraceFieldNames() {
		_, mapped := elasticAPMTraceLabelMapping[field]
		if reason, unsupported := elasticAPMUnsupportedCanonicalFields[field]; unsupported {
			assert.False(t, mapped,
				"canonical field %q is declared unsupported (%s) but is mapped — pick one", field, reason)
			continue
		}
		assert.True(t, mapped, "canonical trace field %q has no Elastic APM field path", field)
	}
}

// TestAPMGroupDimsMatchLabelMapping keeps the grouping dimensions consistent with the
// per-span mapping, so a grouped row's identity is the same string the per-span rows
// show and the same string a filter on that label compares against.
func TestAPMGroupDimsMatchLabelMapping(t *testing.T) {
	assert.NotEmpty(t, elasticAPMTraceGroupDims)

	byKey := map[string][]string{}
	for _, dim := range elasticAPMTraceGroupDims {
		assert.NotEmpty(t, dim.Fields, "dimension %q needs at least one field", dim.Key)
		mapped, ok := elasticAPMTraceLabelMapping[dim.Key]
		assert.True(t, ok, "group dimension %q is not a mapped label", dim.Key)
		// The dimension's FIRST field must be the one the label maps to, or grouping
		// and filtering on the same label read different fields.
		assert.Equal(t, mapped, dim.Fields[0],
			"dimension %q groups on %q but the label filters on %q", dim.Key, dim.Fields[0], mapped)
		byKey[dim.Key] = dim.Fields
	}

	// No dimension may exist for a field this layout cannot answer.
	for field := range elasticAPMUnsupportedCanonicalFields {
		_, grouped := byKey[field]
		assert.False(t, grouped, "%q is declared unsupported but is still a group dimension", field)
	}

	// The WHOLE fallback chain has to match the mapper, not just the first field.
	// An extra trailing fallback here is the subtle version of this bug: a grouped
	// row gets an identity the detail view renders as blank, and only shows up as
	// the two views quietly disagreeing. Checking Fields[0] alone missed exactly
	// that for "resource".
	assert.Equal(t,
		[]string{"url.full", "url.original", "span.destination.service.resource"},
		byKey["resource"],
		"resource grouping must mirror mapAPMHitToTrace's coalesce, which ends at the "+
			"destination and deliberately does NOT fall back to span.name")
	assert.Equal(t, []string{"transaction.name", "span.name"}, byKey["span_name"],
		"span_name mirrors the transaction-or-span coalesce in the mapper")

	// The duration field the grouping averages must be the transaction one: mixing
	// span durations in would average a request against its own sub-calls.
	assert.Equal(t, "transaction.duration.us", apmGroupDurationField)
}
