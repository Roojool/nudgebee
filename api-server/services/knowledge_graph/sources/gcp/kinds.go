package gcp

import (
	"fmt"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// What a GCP inventory row becomes.
//
// This file holds only the mechanics. Which rows a resource claims, the node
// type and concrete label they carry, the subtype and the metadata extraction
// they run are declared in that resource's own file through registerKind, next
// to its schema and its patch spec: compute.go, cloudsql.go, gke.go and so on.
// A row no kind claims becomes a CloudResource, as it did when this was two
// central maps and two switches. Rows that must never become a node at all are
// a different question, answered by shouldSuppressGCPResource — the patch
// policy applies that same predicate before it builds anything.

// resourceKind is one resource's declaration of the rows it builds.
type resourceKind struct {
	// Service is cloud_resourses.service_name as the collector writes it.
	Service string
	// Types are the row types this kind claims, matched lower-cased. Leave it
	// empty to claim every type of Service that no other kind claims.
	Types []string

	NodeType core.NodeType
	// SpecificType is the concrete cloud label. Empty means the node type's name.
	SpecificType string
	// Subtype is the properties["subtype"] value. Empty means the row's type.
	Subtype string

	// Extract pulls this resource's fields out of its parsed meta.
	Extract func(s *GCPSource, properties, metaMap map[string]interface{})
	// Labels pulls them out of the row's normalized labels instead, for
	// resources the collector describes through labels rather than metadata.
	Labels func(s *GCPSource, properties, labels map[string]interface{})

	// Node replaces the generic builder for rows that are a different kind of
	// node altogether (IAM service accounts become ServiceIdentity).
	Node func(s *GCPSource, resource *sources.CloudResourceRow, req *core.SourceBuildRequest) *core.DbNode
}

// KindKey names the rows this kind claims.
func (k resourceKind) KindKey() sources.KindKey {
	return sources.KindKey{Service: k.Service, Types: k.Types}
}

// withTypes returns a copy claiming these row types, so a resource whose exact
// types and whose service fallback resolve the same way declares them once.
func (k resourceKind) withTypes(types ...string) resourceKind {
	k.Types = types
	return k
}

// specificType is the concrete label a node of this kind carries.
func (k resourceKind) specificType() string {
	if k.SpecificType != "" {
		return k.SpecificType
	}
	return string(k.NodeType)
}

var kinds = sources.NewResourceKindRegistry[resourceKind]()

// registerKind declares one GCP resource's rows. Call it from the resource
// file's init().
func registerKind(kind resourceKind) {
	if kind.NodeType == "" || !kind.NodeType.IsKnown() {
		panic(fmt.Sprintf("resource kind for service %q declares unknown node type %q", kind.Service, kind.NodeType))
	}
	kinds.Register(kind)
}

// unclaimedKind is what a row no resource claims becomes.
var unclaimedKind = resourceKind{NodeType: core.NodeTypeCloudResource}

// kindFor resolves a row: the resource that claims this exact (type, service),
// else the one that claims the service, else the CloudResource catch-all.
func kindFor(resourceType, serviceName string) resourceKind {
	if kind, ok := kinds.LookupType(resourceType, serviceName); ok {
		return kind
	}
	if kind, ok := kinds.LookupService(serviceName); ok {
		return kind
	}
	return unclaimedKind
}

// determineNodeType determines the knowledge graph node type from a GCP
// resource type and service name.
func (s *GCPSource) determineNodeType(resourceType, serviceName string) core.NodeType {
	return kindFor(resourceType, serviceName).NodeType
}

// determineSpecificType resolves the concrete GCP label for a resource,
// defaulting to the ontology node type's name so it is never blank.
func (s *GCPSource) determineSpecificType(nodeType core.NodeType, serviceName, resourceType string) string {
	if label := kindFor(resourceType, serviceName).SpecificType; label != "" {
		return label
	}
	return string(nodeType)
}
