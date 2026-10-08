package observability

import (
	"testing"

	"nudgebee/services/integrations"

	"github.com/stretchr/testify/assert"
)

// TestIntegrationSchemaMatchesESTraceSchemas guards the one piece of duplication this
// design cannot avoid: the accepted values are declared in the integrations package
// (where the config schema lives) and re-exported as a typed enum here, because
// observability imports integrations and not the reverse. If someone adds a layout on
// one side only, the UI and the dispatcher disagree — the dropdown offers a value that
// resolves to the default reader and silently returns zero hits.
func TestIntegrationSchemaMatchesESTraceSchemas(t *testing.T) {
	schema := integrations.Elasticsearch{}.ConfigSchema()
	prop, ok := schema.Properties["trace_schema"]
	assert.True(t, ok, "the ES integration must declare a trace_schema property")

	declared := make([]string, 0, len(prop.Enum))
	for _, v := range prop.Enum {
		s, isStr := v.(string)
		assert.True(t, isStr, "trace_schema enum values must be strings, got %T", v)
		declared = append(declared, s)
	}

	assert.ElementsMatch(t, ESTraceSchemaNames(), declared,
		"integrations.ESTraceSchemas and observability.AllESTraceSchemas have drifted")
	assert.Equal(t, string(ESTraceSchemaDefault), prop.Default,
		"the schema default must be the layout resolveESTraceSchema falls back to")

	// A property with no Description renders nothing in the integration form
	// (IntegrationDynamicFormModal returns null early), so the field would be
	// invisible and unsettable.
	assert.NotEmpty(t, prop.Description, "trace_schema needs a Description or the UI drops the field")
}

func TestESTraceSchemaValid(t *testing.T) {
	for _, s := range AllESTraceSchemas {
		assert.True(t, s.Valid(), "%q should be valid", s)
	}
	assert.False(t, ESTraceSchema("").Valid())
	assert.False(t, ESTraceSchema("elastic-apm").Valid(), "hyphenated spelling is not the stored form")
	assert.False(t, ESTraceSchema("ELASTIC_APM").Valid(), "matching is exact, not case-insensitive")
	assert.False(t, ESTraceSchema("otel").Valid())
}

// TestEsTraceSourceFor pins the schema-to-reader mapping. The three readers share no
// field names, so picking the wrong one returns zero hits rather than an error — which
// is why this mapping is worth asserting directly.
func TestEsTraceSourceFor(t *testing.T) {
	assert.IsType(t, &ElasticSaasTraceSource{}, esTraceSourceFor(ESTraceSchemaDataPrepper))
	assert.IsType(t, &ElasticOtelTraceSource{}, esTraceSourceFor(ESTraceSchemaOtelNative))
	assert.IsType(t, &ElasticAPMTraceSource{}, esTraceSourceFor(ESTraceSchemaElasticAPM))

	// An empty or unrecognised schema falls back to the historical default rather
	// than failing, matching what the reader did before the property existed.
	assert.IsType(t, &ElasticSaasTraceSource{}, esTraceSourceFor(ESTraceSchema("")))
	assert.IsType(t, &ElasticSaasTraceSource{}, esTraceSourceFor(ESTraceSchema("nonsense")))
}

// TestESTraceSchemaNames keeps the error-message helper in step with the enum.
func TestESTraceSchemaNames(t *testing.T) {
	assert.Equal(t, []string{"data_prepper", "otel_native", "elastic_apm"}, ESTraceSchemaNames())
}

// TestAllESTraceSourcesImplementTraceSource is a compile-time assertion that the new
// reader satisfies the full 13-method interface; a missing method would otherwise only
// surface where esTraceSourceFor is called.
func TestAllESTraceSourcesImplementTraceSource(t *testing.T) {
	var sources = []TraceSource{
		&ElasticSaasTraceSource{},
		&ElasticOtelTraceSource{},
		&ElasticAPMTraceSource{},
	}
	assert.Len(t, sources, len(AllESTraceSchemas), "every declared schema needs a reader")

	// Each must advertise a non-empty label mapping: providerDeclaresTraceFields
	// gates whether the canonical trace fields are advertised to the UI and the
	// agent on exactly this, so an empty mapping silently hides every field.
	for _, src := range sources {
		assert.NotEmpty(t, src.GetLabelMapping(), "%T must declare a label mapping", src)
		assert.NotEmpty(t, src.GetSupportedOperators(), "%T must declare operators", src)
	}
}
