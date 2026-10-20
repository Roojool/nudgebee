package gcp

// GCP dispatch-completeness (Tier A): every resource kind declared by a GCP
// resource file must resolve to the node type and label it declares, through
// the same lookup the node builder uses.

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

const sentinelUnknownType = "__e2e_nonexistent_type__"

func TestE2E_GCPDispatchCompleteness(t *testing.T) {
	s, err := NewGCPSource(GCPSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGCPSource: %v", err)
	}

	checked := 0
	for _, kind := range kinds.Kinds() {
		key := kind.KindKey()
		probes := key.Types
		if len(probes) == 0 {
			probes = []string{sentinelUnknownType}
		}
		for _, resourceType := range probes {
			if got := s.determineNodeType(resourceType, key.Service); got != kind.NodeType {
				t.Errorf("gcp [%s / %s]: want %s, got %s", resourceType, key.Service, kind.NodeType, got)
			}
			if got := s.determineSpecificType(kind.NodeType, key.Service, resourceType); got != kind.specificType() {
				t.Errorf("gcp [%s / %s]: want specific type %s, got %s", resourceType, key.Service, kind.specificType(), got)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no GCP resource kinds registered")
	}
	t.Logf("checked %d (type, service) keys across %d kinds", checked, len(kinds.Kinds()))

	if got := s.determineNodeType(sentinelUnknownType, "__e2e_nonexistent_service__"); got != core.NodeTypeCloudResource {
		t.Errorf("gcp unknown fallthrough: want %s, got %s", core.NodeTypeCloudResource, got)
	}
}
