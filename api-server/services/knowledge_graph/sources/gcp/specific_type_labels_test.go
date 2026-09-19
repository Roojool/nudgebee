package gcp

import "nudgebee/services/knowledge_graph/core"

// gcpSpecificTypeLabels are the concrete labels a GCP node can carry: the one
// each resource kind declares, plus the ones declared for nodes the CLI
// enrichment synthesizes. A kind that declares no label emits its node type's
// name, which is the ontology type rather than a concrete one.
func gcpSpecificTypeLabels() []string {
	seen := map[string]bool{}
	labels := make([]string, 0, len(kinds.Kinds()))
	add := func(label string) {
		if label == "" || seen[label] || core.NodeType(label).IsKnown() {
			return
		}
		seen[label] = true
		labels = append(labels, label)
	}
	for _, kind := range kinds.Kinds() {
		add(kind.specificType())
	}
	// Labels of resources the graph learns about from the gcloud enrichment
	// rather than from an inventory row: DNS zones (dnszone.go), Cloud NAT and
	// external IPs (catalog.go). Their builders do not stamp specific_type
	// today, so these are the labels their schemas reserve.
	add("CloudDNSZone")
	add("CloudNATGateway")
	add("GCPExternalIP")
	// The Storage fallback label, kept declared for a Storage kind that names no
	// label of its own.
	add("GCPStorage")
	return labels
}
