package k8s

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

// Three places list Kubernetes kinds: the kinds this package declares, the
// canonical spellings core normalizes observed traffic to, and the parent kinds
// a synthesized Pod can name. core cannot import this package, so the lists
// cannot be one; these tests make a drift between them fail here.

func TestSupportedManagesKindsAreDeclaredWorkloads(t *testing.T) {
	for _, kind := range core.SupportedManagesKinds() {
		declared := workloadKindFor(kind)
		if declared.Kind == "" {
			t.Errorf("core says a Pod can be managed by a %s, but no kind declares it", kind)
			continue
		}
		if !declared.Inventory {
			t.Errorf("core says a Pod can be managed by a %s, but the account build does not query for it", kind)
		}
		if declared.NodeType != core.NodeTypeWorkload {
			t.Errorf("core says a Pod can be managed by a %s, but its rows build %s nodes, which a MANAGES edge cannot resolve to",
				kind, declared.NodeType)
		}
	}
}

func TestCanonicalWorkloadKindsAreDeclared(t *testing.T) {
	for _, kind := range core.CanonicalWorkloadKinds() {
		if workloadKindFor(kind).Kind == "" {
			t.Errorf("core normalizes observed traffic to kind %s, which no kind declares — its %q specific_type would match no node",
				kind, workloadSpecificType(kind))
		}
	}
}

func TestDeclaredKindsAreSelfConsistent(t *testing.T) {
	for _, kind := range workloadKinds.Kinds() {
		if _, ok := core.LookupSpecificTypeSchema(workloadSpecificType(kind.Kind)); !ok && kind.Inventory {
			t.Errorf("kind %s is part of the inventory but %q has no concrete schema",
				kind.Kind, workloadSpecificType(kind.Kind))
		}
	}
}
