package azure

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// TestSpecificTypeHasOntologySpec is the forcing function for Azure, mirroring
// the AWS one: every concrete specific_type label an Azure node can carry must
// have a matching ontology spec registered in core/ontology_data_azure.go.
func TestSpecificTypeHasOntologySpec(t *testing.T) {
	// AzureContainerInstance declares a schema but no ontology spec: container
	// groups were added to the graph without one, which this test found. Adding
	// it changes what those nodes carry, so it is a change of its own.
	skip := map[string]bool{"AzureContainerInstance": true}
	for _, label := range azureSpecificTypeLabels() {
		if skip[label] {
			continue
		}
		if _, ok := core.LookupOntologySpec(label); !ok {
			t.Errorf("Azure specific_type %q has no ontology registry spec — add it to core/ontology_data_azure.go", label)
		}
	}
}
