package aws

// AWS dispatch-completeness (Tier A): every resource kind declared by an AWS
// resource file must resolve to the node type it declares, through the same
// lookup the node builder uses. Lives in package aws because it reads the
// unexported kind registry.

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// sentinelUnknownType is a resource type guaranteed absent from the registry,
// used to force a lookup down its service-name fallback path.
const sentinelUnknownType = "__e2e_nonexistent_type__"

func TestE2E_AWSDispatchCompleteness(t *testing.T) {
	s, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}

	checked := 0
	for _, kind := range kinds.Kinds() {
		key := kind.KindKey()
		probes := key.Types
		if len(probes) == 0 {
			// A kind that claims every other type of its service is reached
			// with a type nothing claims.
			probes = []string{sentinelUnknownType}
		}
		for _, resourceType := range probes {
			if got := s.determineNodeType(resourceType, key.Service); got != kind.NodeType {
				t.Errorf("aws [%s / %s]: want %s, got %s", resourceType, key.Service, kind.NodeType, got)
			}
			if got := s.determineSpecificType(resourceType, key.Service, kind.NodeType); got != kind.specificType() {
				t.Errorf("aws [%s / %s]: want specific type %s, got %s", resourceType, key.Service, kind.specificType(), got)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no AWS resource kinds registered")
	}
	t.Logf("checked %d (type, service) keys across %d kinds", checked, len(kinds.Kinds()))

	// An entirely unknown (type, service) must degrade to CloudResource.
	if got := s.determineNodeType(sentinelUnknownType, "__e2e_nonexistent_service__"); got != core.NodeTypeCloudResource {
		t.Errorf("aws unknown fallthrough: want %s, got %s", core.NodeTypeCloudResource, got)
	}
}
