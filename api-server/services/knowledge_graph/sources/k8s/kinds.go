package k8s

import (
	"fmt"
	"sort"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// What a Kubernetes workload row becomes.
//
// This file holds only the mechanics. What each kind is — the node type its
// rows build, whether the account build queries for it, whether the change
// detector watches it, whether one of its rows can be patched on its own, and
// which spec and status fields its nodes keep — is declared in that kind's own
// file through registerWorkloadKind, next to its schema and its edge builder:
// workload.go, service.go, ingress.go, pvc.go, configmap.go, secret.go. A kind
// nothing declares builds a generic Workload node, as it did when this was
// three switches and three lists.

// workloadKind is one Kubernetes kind's declaration of its rows.
type workloadKind struct {
	// Kind is k8s_workloads.kind in the casing Kubernetes uses; matched
	// case-insensitively, as the collectors are inconsistent about it.
	Kind string

	NodeType core.NodeType

	// Inventory declares that the account build queries for these rows. Kinds
	// the relay reports instead are still declared here for what they build.
	Inventory bool
	// Churning declares that this kind's rows come and go constantly, so a
	// change in their number is not worth reacting to: the account build
	// refreshes them anyway.
	Churning bool
	// Patchable declares that a node and its own edges come entirely from one
	// row, so a change to it can be applied without rebuilding the account.
	// Kinds the relay or other rows also shape are not patchable.
	Patchable bool

	// Extract pulls the fields this kind keeps out of its row's spec. It is
	// called only when the row has a spec object.
	Extract func(s *K8sSource, properties, spec, metaMap map[string]interface{})
}

// KindKey names the rows this kind claims. Kubernetes rows carry no service, so
// the kind is the whole key.
func (k workloadKind) KindKey() sources.KindKey {
	return sources.KindKey{Types: []string{k.Kind}}
}

var workloadKinds = sources.NewResourceKindRegistry[workloadKind]()

// registerWorkloadKind declares one Kubernetes kind. Call it from the kind's
// own file's init().
func registerWorkloadKind(kind workloadKind) {
	if kind.Kind == "" {
		panic("workload kind declares no kind")
	}
	if kind.NodeType == "" || !kind.NodeType.IsKnown() {
		panic(fmt.Sprintf("workload kind %q declares unknown node type %q", kind.Kind, kind.NodeType))
	}
	if kind.Churning && kind.Patchable {
		panic(fmt.Sprintf("workload kind %q is both churning and patchable: a kind the detector ignores cannot be patched", kind.Kind))
	}
	workloadKinds.Register(kind)
}

// unclaimedWorkloadKind is what a kind nothing declares builds: a generic
// workload, the shape the account build has always fallen back to.
var unclaimedWorkloadKind = workloadKind{NodeType: core.NodeTypeWorkload}

// workloadKindFor resolves a row's kind.
func workloadKindFor(kind string) workloadKind {
	if declared, ok := workloadKinds.LookupType(kind, ""); ok {
		return declared
	}
	return unclaimedWorkloadKind
}

// defaultWorkloadKinds are the kinds the account build queries k8s_workloads
// for, in a stable order.
func defaultWorkloadKinds() []string {
	out := make([]string, 0, len(workloadKinds.Kinds()))
	for _, kind := range workloadKinds.Kinds() {
		if kind.Inventory {
			out = append(out, kind.Kind)
		}
	}
	sort.Strings(out)
	return out
}

// detectedWorkloadKinds returns the kinds of the graph's workloads the change
// detector watches: the ones that do not churn on their own.
func detectedWorkloadKinds(graphKinds []string) []string {
	out := make([]string, 0, len(graphKinds))
	for _, kind := range graphKinds {
		if !workloadKindFor(kind).Churning {
			out = append(out, kind)
		}
	}
	return out
}

// workloadSpecificType is the specific type a workload kind's node carries
// (createNodeFromWorkload, and the Service/Ingress builders).
func workloadSpecificType(kind string) string {
	return "Kubernetes" + kind
}

// clusterScopedKeyRule is how one cluster-scoped node type's unique key differs
// from a namespaced resource's: it has no namespace, and its name may have to
// come from the cluster or namespace it describes.
type clusterScopedKeyRule struct {
	NameFallback func(node *core.DbNode) string
}

var clusterScopedKeyRules = map[core.NodeType]clusterScopedKeyRule{}

// registerClusterScopedKind declares that this node type is scoped to the
// cluster rather than to a namespace. Call it from the resource's own file.
func registerClusterScopedKind(nodeType core.NodeType, rule clusterScopedKeyRule) {
	if _, exists := clusterScopedKeyRules[nodeType]; exists {
		panic(fmt.Sprintf("cluster-scoped key rule for %q is already registered", nodeType))
	}
	clusterScopedKeyRules[nodeType] = rule
}
