package azure

import (
	"nudgebee/services/knowledge_graph/core"
)

// Azure providers the graph records but has no resource file for: the rows
// carry no metadata we extract and no edges of their own beyond the reference
// edges every Azure resource gets, so there is nothing to put in a file of
// their own beyond what they are. Resources with real behaviour live in their
// own file — vm.go, aks.go, networking.go and so on.

// The inventory rows these providers claim (see kinds.go).
func init() {
	registerKind(resourceKind{
		Types:    []string{"botservices"},
		NodeType: core.NodeTypeAIService,
	})
	registerKind(resourceKind{
		Service:  "microsoft.cognitiveservices",
		NodeType: core.NodeTypeAIService,
	})
	registerKind(resourceKind{
		Service:  "microsoft.databricks",
		NodeType: core.NodeTypeAIService,
	})
	registerKind(resourceKind{
		Service:  "microsoft.machinelearningservices",
		NodeType: core.NodeTypeAIService,
	})
	registerKind(resourceKind{
		Service:  "microsoft.communication",
		NodeType: core.NodeTypeEmailService,
	})
	registerKind(resourceKind{
		Types:    []string{"eventhubnamespace", "namespaces", "servicebusnamespace"},
		NodeType: core.NodeTypeMessageQueue,
	})
	registerKind(resourceKind{
		Service:  "microsoft.eventhub",
		NodeType: core.NodeTypeMessageQueue,
	})
	registerKind(resourceKind{
		Service:  "microsoft.servicebus",
		NodeType: core.NodeTypeMessageQueue,
	})
	registerKind(resourceKind{
		Types:    []string{"systemtopics"},
		NodeType: core.NodeTypeTopic,
	})
}
