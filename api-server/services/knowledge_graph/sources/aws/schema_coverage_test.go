package aws

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// TestSpecificTypeHasSchema is the forcing function for the AWS concrete-schema
// layer: every concrete specific_type label the source can emit must have a
// registered core.SpecificTypeSchema. A newly-added AWS resource type therefore
// cannot ship a specific_type without also declaring its concrete property
// schema, which drives query_attributes.
func TestSpecificTypeHasSchema(t *testing.T) {
	for _, label := range awsSpecificTypeLabels() {
		if _, ok := core.LookupSpecificTypeSchema(label); !ok {
			t.Errorf("AWS specific_type %q has no concrete schema — declare a core.SpecificTypeSchema and register it in its sources/aws/*.go init()", label)
		}
	}
}
