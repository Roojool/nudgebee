package azure

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// azureSpecificTypeLabels are the concrete labels an Azure node can carry: the
// ones the resource kinds declare, plus the ones declared for nodes a builder
// synthesizes rather than reading from an inventory row. A kind that declares
// no label emits its node type's name, which is the ontology type rather than a
// concrete one.
func azureSpecificTypeLabels() []string {
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
		add(kind.specificType(&sources.CloudResourceRow{}))
		// A kind whose label depends on the row (Cosmos vs SQL) declares both.
		if kind.Label != nil {
			add(kind.Label(&sources.CloudResourceRow{ServiceName: "microsoft.documentdb/databaseAccounts"}))
			add(kind.Label(&sources.CloudResourceRow{ServiceName: "microsoft.sql/servers"}))
		}
	}
	// Labels of nodes an Azure builder synthesizes: load-balancer backend pools
	// (loadbalancer.go), key-vault keys (keyvault.go), API Management gateways
	// (apimanagement.go) and the monitoring/log workspaces (monitor.go).
	add("AzureBackendPool")
	add("AzureKeyVaultKey")
	add("AzureAPIManagement")
	add("AzureMonitor")
	add("AzureLogAnalytics")
	return labels
}
