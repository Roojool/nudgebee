package azure

// Azure dispatch-completeness (Tier A): every resource kind declared by an
// Azure resource file must resolve to the node type it declares, through the
// same lookup the node builder uses — probed both with the bare row type and
// with the provider-qualified form the collectors also write.

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

const sentinelUnknownType = "__e2e_nonexistent_type__"

func TestE2E_AzureDispatchCompleteness(t *testing.T) {
	s, err := NewAzureSource(AzureSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAzureSource: %v", err)
	}

	checked := 0
	for _, kind := range kinds.Kinds() {
		key := kind.KindKey()
		if len(key.Types) == 0 {
			service := key.Service + "/somekind"
			if got := s.determineNodeType(sentinelUnknownType, service); got != kind.NodeType {
				t.Errorf("azure provider [%s]: want %s, got %s", service, kind.NodeType, got)
			}
			checked++
			continue
		}
		for _, rowType := range key.Types {
			for _, probe := range []string{rowType, "Microsoft.Something/" + rowType} {
				if got := s.determineNodeType(probe, "microsoft.notaprovider"); got != kind.NodeType {
					t.Errorf("azure [%s]: want %s, got %s", probe, kind.NodeType, got)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Azure resource kinds registered")
	}
	t.Logf("checked %d probes across %d kinds", checked, len(kinds.Kinds()))

	if got := s.determineNodeType(sentinelUnknownType, "microsoft.notaprovider"); got != core.NodeTypeCloudResource {
		t.Errorf("azure unknown fallthrough: want %s, got %s", core.NodeTypeCloudResource, got)
	}
}
