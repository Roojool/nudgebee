package aws

import "nudgebee/services/knowledge_graph/core"

// awsSpecificTypeLabels are the concrete labels the AWS source can emit: the one
// each resource kind declares, plus the identity labels the IAM builders set
// directly. A kind that declares no label emits its node type's name, which is
// the ontology type rather than a concrete one and has no schema of its own.
func awsSpecificTypeLabels() []string {
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
	// Set by buildServiceIdentityNodes (iam.go) rather than by a kind: IAM role
	// rows are suppressed and re-emitted as typed identities.
	add("IAMRole")
	return labels
}
