package gcp

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// TestSpecificTypeHasOntologySpec is the forcing function for GCP, mirroring the
// AWS one: every concrete specific_type label a GCP node can carry must have a
// matching ontology spec registered in core/ontology_data_gcp.go, so a new GCP
// resource kind cannot ship a specific_type without its normalized ontology
// schema.
func TestSpecificTypeHasOntologySpec(t *testing.T) {
	// GCPStorage is the label a Storage kind falls back to when it names none;
	// no kind uses it today and it has no ontology spec.
	skip := map[string]bool{"GCPStorage": true}
	for _, label := range gcpSpecificTypeLabels() {
		if skip[label] {
			continue
		}
		if _, ok := core.LookupOntologySpec(label); !ok {
			t.Errorf("GCP specific_type %q has no ontology registry spec — add it to core/ontology_data_gcp.go", label)
		}
	}
}
