package integrations

import (
	"testing"

	"nudgebee/services/integrations/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// esValidConnection is the minimum that gets past the URL and auth checks, so the
// assertions below are about trace_schema and nothing else. Every case here must
// produce at least one error: ValidateConfig returns early on errs, and only reaches
// the live _cluster/health probe when there are none.
func esValidConnection() []core.IntegrationConfigValue {
	return []core.IntegrationConfigValue{
		cv("url", "https://es.example.com"),
		cv("auth_type", "basic"),
		cv("username", "elastic"),
		cv("password", "secret"),
	}
}

// TestElasticsearch_ValidateConfig_RejectsUnknownTraceSchema covers the hand-written
// enum check. There is no generic server-side enum validator, so without this an
// arbitrary value would be stored and then fall back to the default reader at query
// time — returning zero hits with nothing to explain why.
func TestElasticsearch_ValidateConfig_RejectsUnknownTraceSchema(t *testing.T) {
	errs := Elasticsearch{}.ValidateConfig(nil,
		append(esValidConnection(), cv("trace_schema", "elastic-apm")), "acc")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "trace_schema must be one of")
	assert.Contains(t, errs[0].Error(), "elastic-apm", "the rejected value should be echoed back")
	// The message must list what IS accepted, so the operator can fix it.
	assert.Contains(t, errs[0].Error(), "elastic_apm")
}

// TestElasticsearch_ValidateConfig_RejectsPerAccountTraceSchema covers the same value
// inside the per-account index_account_mapping blob, which is a separate code path.
func TestElasticsearch_ValidateConfig_RejectsPerAccountTraceSchema(t *testing.T) {
	errs := Elasticsearch{}.ValidateConfig(nil,
		append(esValidConnection(), cv("index_account_mapping",
			`[{"account_id":"acc-a","trace_index":"traces-apm*","trace_schema":"apm"}]`)), "acc")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "index_account_mapping[0] trace_schema must be one of")
}

// TestElasticsearch_ValidateConfig_TraceSchemaCaseSensitive pins that matching is
// exact. The stored value is dispatched on by string equality, so an accepted
// "Elastic_APM" would not resolve to a reader.
func TestElasticsearch_ValidateConfig_TraceSchemaCaseSensitive(t *testing.T) {
	errs := Elasticsearch{}.ValidateConfig(nil,
		append(esValidConnection(), cv("trace_schema", "Elastic_APM")), "acc")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "trace_schema must be one of")
}

// TestESTraceSchemasAreDeclaredInSchema ties the accepted set to what the form offers.
// The create path rejects any config key absent from ConfigSchema outright, and the
// dropdown is built from this Enum, so a value accepted by ValidateConfig but missing
// from the enum would be unreachable from the UI.
func TestESTraceSchemasAreDeclaredInSchema(t *testing.T) {
	prop, ok := Elasticsearch{}.ConfigSchema().Properties["trace_schema"]
	require.True(t, ok, "trace_schema must be declared or no value for it can ever be saved")

	declared := make([]string, 0, len(prop.Enum))
	for _, v := range prop.Enum {
		s, isStr := v.(string)
		require.True(t, isStr)
		declared = append(declared, s)
	}
	assert.ElementsMatch(t, ESTraceSchemas, declared)
	assert.Equal(t, ESTraceSchemaDefault, prop.Default)
	assert.Equal(t, ESTraceSchemaDataPrepper, ESTraceSchemaDefault,
		"the default must stay Data Prepper: it is what the reader fell back to before this property existed")

	// Only shown when ES is the trace provider, like trace_index above it.
	assert.Equal(t, map[string]any{core.DefaultTraceProvider: true}, prop.ShowWhen)
}

// TestElasticsearchAgentHasNoTraceSchema records a deliberate omission: agent-sourced
// ES never serves traces (getTraceSource matches on integrationSource == "user"), so
// the property would be dead config on that schema.
func TestElasticsearchAgentHasNoTraceSchema(t *testing.T) {
	_, ok := ElasticsearchAgent{}.ConfigSchema().Properties["trace_schema"]
	assert.False(t, ok, "agent-sourced ES has no trace reader to select")
}
