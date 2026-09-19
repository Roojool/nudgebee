package gcp

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// TestSpecificTypeHasSchema is the coverage guardrail for the GCP source: every
// concrete specific_type label a GCP node can carry must have a registered
// concrete-property schema (core.SpecificTypeSchema) co-located in this
// package. A newly-added GCP resource kind therefore cannot ship a
// specific_type without also declaring its concrete schema, which in turn
// drives query_attributes.
func TestSpecificTypeHasSchema(t *testing.T) {
	for _, label := range gcpSpecificTypeLabels() {
		if _, ok := core.LookupSpecificTypeSchema(label); !ok {
			t.Errorf("GCP specific_type %q has no concrete schema — declare a core.SpecificTypeSchema and RegisterSpecificTypeSchema in its sources/gcp/*.go file", label)
		}
	}
}
