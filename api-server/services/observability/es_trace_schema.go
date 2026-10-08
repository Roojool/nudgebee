package observability

import (
	"log/slog"

	"nudgebee/services/integrations"
	"nudgebee/services/security"
)

// ESTraceSchema is the trace layout an Elasticsearch cluster stores. The three
// layouts share no field names, so a reader written for one returns zero hits
// against another — which is why this is declared on the integration instead of
// being inferred.
//
// It replaced isOtelNativeTraceIndex, which guessed the layout from the index
// name. That guess mis-read Elastic APM (traces-apm* matched its "traces-" prefix
// rule and selected the OTel reader), and it could not run at all in the four
// resolveTraceSource callers that have no index to inspect — see
// resolveESTraceSchema.
type ESTraceSchema string

const (
	// ESTraceSchemaDataPrepper is Data Prepper's otel-v1-apm-span-* schema: flat
	// traceId / spanId / durationInNanos, attributes under span.attributes.*@*.
	// Read by ElasticSaasTraceSource. The historical default.
	ESTraceSchemaDataPrepper ESTraceSchema = integrations.ESTraceSchemaDataPrepper
	// ESTraceSchemaOtelNative is the OTel-native traces-*.otel data streams written
	// by the OTel Collector's elasticsearch exporter under mapping.mode: otel —
	// trace_id / span_id / duration with resource.attributes.*. Read by
	// ElasticOtelTraceSource.
	ESTraceSchemaOtelNative ESTraceSchema = integrations.ESTraceSchemaOtelNative
	// ESTraceSchemaElasticAPM is Elastic's own traces-apm* layout written by APM
	// Server — processor.event, transaction.duration.us, span.destination.service.*,
	// ECS kubernetes.* fields. Read by ElasticAPMTraceSource.
	ESTraceSchemaElasticAPM ESTraceSchema = integrations.ESTraceSchemaElasticAPM
)

// ESTraceSchemaDefault is the layout assumed when an integration carries no
// trace_schema value. The backfill migration gives every existing integration an
// explicit value, so this only applies to a row written outside the normal save path.
const ESTraceSchemaDefault ESTraceSchema = integrations.ESTraceSchemaDefault

// AllESTraceSchemas is the set offered in the integration schema's enum and
// accepted by Elasticsearch.ValidateConfig. Keep in sync with the "trace_schema"
// property in integrations/elasticsearch.go.
var AllESTraceSchemas = []ESTraceSchema{
	ESTraceSchemaDataPrepper,
	ESTraceSchemaOtelNative,
	ESTraceSchemaElasticAPM,
}

// Valid reports whether s is one of the three known layouts. Used to ignore a
// stale or hand-edited value rather than dispatch on it.
func (s ESTraceSchema) Valid() bool {
	for _, known := range AllESTraceSchemas {
		if s == known {
			return true
		}
	}
	return false
}

// ESTraceSchemaNames returns the enum values as strings, for the integration schema
// declaration and for error messages that list what is accepted.
func ESTraceSchemaNames() []string {
	names := make([]string, 0, len(AllESTraceSchemas))
	for _, s := range AllESTraceSchemas {
		names = append(names, string(s))
	}
	return names
}

// resolveESTraceSchema returns the trace layout declared for an account, applying
// the same precedence as the trace index: the per-account index_account_mapping
// entry wins over the integration's top-level trace_schema, which wins over the
// default. GetElasticsearchConfig has already collapsed those two into
// cfg.TraceSchema by the time this reads it.
//
// Unlike the index, this takes no per-request override: the Traces-tab picker
// selects an index but carries no layout, so a picked index is read with the
// account's declared schema.
//
// A config lookup failure is non-fatal and yields the default, matching the
// behaviour resolveTraceSource had when it could not read the config.
func resolveESTraceSchema(ctx *security.RequestContext, accountId string) ESTraceSchema {
	cfg, err := GetElasticsearchConfig(ctx, accountId)
	if err != nil {
		slog.Warn("observability: could not read ES config for trace schema; assuming default",
			"account_id", accountId, "schema", ESTraceSchemaDefault, "error", err)
		return ESTraceSchemaDefault
	}
	if cfg.TraceSchema.Valid() {
		return cfg.TraceSchema
	}
	if cfg.TraceSchema != "" {
		// A value is stored but is not one we know — a hand-edited row, or a value
		// written by a newer build. Dispatching on it would pick a reader at random,
		// so fall back and say so loudly: this is a misconfiguration, not a default.
		slog.Warn("observability: unknown ES trace_schema value; assuming default",
			"account_id", accountId, "value", cfg.TraceSchema,
			"accepted", ESTraceSchemaNames(), "schema", ESTraceSchemaDefault)
	}
	return ESTraceSchemaDefault
}

// esTraceSourceFor returns the reader for a declared layout.
func esTraceSourceFor(schema ESTraceSchema) TraceSource {
	switch schema {
	case ESTraceSchemaElasticAPM:
		return &ElasticAPMTraceSource{}
	case ESTraceSchemaOtelNative:
		return &ElasticOtelTraceSource{}
	default:
		return &ElasticSaasTraceSource{}
	}
}
