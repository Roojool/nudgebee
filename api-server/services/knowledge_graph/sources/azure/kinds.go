package azure

import (
	"fmt"
	"strings"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// What an Azure inventory row becomes.
//
// This file holds only the mechanics. Which rows a resource claims, the node
// type and concrete label they carry and the metadata extraction they run are
// declared in that resource's own file through registerKind, next to its schema
// and its patch spec: vm.go, aks.go, networking.go, database.go and so on. A
// row no kind claims becomes a CloudResource, as it did when this was two
// central maps and a switch.
//
// Azure rows are identified by their type first: the collector writes both the
// bare segment ("virtualmachines") and the provider-qualified form
// ("Microsoft.Compute/virtualMachines"), and the service name only decides
// which provider a row came from. So a resource claims row types with no
// service, and a provider claims everything else under its prefix.
type resourceKind struct {
	// Service is the lower-cased provider prefix of cloud_resourses.service_name
	// ("microsoft.compute"), for a kind that claims a whole provider. Leave it
	// empty for a kind that claims row types.
	Service string
	// Types are the row types this kind claims, matched lower-cased against the
	// whole type and against its last "/"-delimited segment.
	Types []string

	NodeType core.NodeType
	// SpecificType is the concrete cloud label. Empty means the node type's name.
	SpecificType string
	// Label resolves the label from the row instead, for the resources whose
	// flavour is only visible in the provider that reported them.
	Label func(resource *sources.CloudResourceRow) string

	// Extract pulls this resource's fields out of its parsed meta. It receives
	// the Resource Graph "properties" object, already unwrapped.
	Extract func(s *AzureSource, properties, propsMap map[string]interface{})
	// KeepRawMeta keeps the row's raw meta on the node, for resources whose
	// children are embedded in it (a VNet's subnets).
	KeepRawMeta bool
}

// KindKey names the rows this kind claims.
func (k resourceKind) KindKey() sources.KindKey {
	return sources.KindKey{Service: k.Service, Types: k.Types}
}

// specificType is the concrete label a node of this kind carries.
func (k resourceKind) specificType(resource *sources.CloudResourceRow) string {
	if k.Label != nil {
		if label := k.Label(resource); label != "" {
			return label
		}
	}
	if k.SpecificType != "" {
		return k.SpecificType
	}
	return string(k.NodeType)
}

var kinds = sources.NewResourceKindRegistry[resourceKind]()

// registerKind declares one Azure resource's rows. Call it from the resource
// file's init().
func registerKind(kind resourceKind) {
	if kind.NodeType == "" || !kind.NodeType.IsKnown() {
		panic(fmt.Sprintf("resource kind for %q declares unknown node type %q", kind.Service, kind.NodeType))
	}
	kinds.Register(kind)
}

// unclaimedKind is what a row no resource claims becomes.
var unclaimedKind = resourceKind{NodeType: core.NodeTypeCloudResource}

// kindFor resolves a row: the resource that claims this row type, then the one
// that claims its last path segment (collectors that store the
// provider-qualified form), then the provider's own kind, else the
// CloudResource catch-all.
func kindFor(resourceType, serviceName string) resourceKind {
	lowered := strings.ToLower(resourceType)
	if kind, ok := kinds.LookupType(lowered, ""); ok {
		return kind
	}
	if idx := strings.LastIndex(lowered, "/"); idx >= 0 && idx < len(lowered)-1 {
		if kind, ok := kinds.LookupType(lowered[idx+1:], ""); ok {
			return kind
		}
	}
	if kind, ok := kinds.LookupService(strings.ToLower(extractServicePrefix(serviceName))); ok {
		return kind
	}
	return unclaimedKind
}

// determineNodeType maps an Azure resource type to a knowledge graph node type.
func (s *AzureSource) determineNodeType(resourceType, serviceName string) core.NodeType {
	return kindFor(resourceType, serviceName).NodeType
}
