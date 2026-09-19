package aws

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// TestSpecificTypeHasOntologySpec is the forcing function for AWS: every concrete
// specific_type label the source can emit must have a matching ontology spec
// registered in core/ontology_data_aws.go. A newly-added AWS resource type
// therefore cannot ship a specific_type without also declaring its normalized
// ontology schema.
func TestSpecificTypeHasOntologySpec(t *testing.T) {
	for _, label := range awsSpecificTypeLabels() {
		if _, ok := core.LookupOntologySpec(label); !ok {
			t.Errorf("AWS specific_type %q has no ontology registry spec — add it to core/ontology_data_aws.go", label)
		}
	}
}
