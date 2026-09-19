package aws

import (
	"fmt"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// What an AWS inventory row becomes.
//
// This file holds only the mechanics. Which rows a resource claims, the node
// type and concrete label they carry, the subtype, the static properties and
// the metadata extractor that runs are declared in that resource's own file
// through registerKind, next to its schema, its edge builder and its patch
// spec: ec2.go, rds.go, sqs.go, s3.go and so on. A row no kind claims becomes
// a CloudResource, as it did when this was two central maps.

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
	// Props are stamped after extraction (queue_type, cache_type, managed).
	Props map[string]interface{}

	// Extract pulls this resource's fields out of its parsed meta. It runs
	// after extractCommonMetadataFields and before synthesizeAWSEndpointDNS.
	Extract func(s *AWSSource, properties, metaMap map[string]interface{})

	// Suppress drops the row: it is already emitted as another node (IAM roles
	// become typed ServiceIdentity nodes).
	Suppress bool
	// Node replaces the generic builder for rows that are a different kind of
	// node altogether (IAM users become ServiceIdentity).
	Node func(s *AWSSource, resource *sources.CloudResourceRow, req *core.SourceBuildRequest) *core.DbNode
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

// registerKind declares one AWS resource's rows. Call it from the resource
// file's init().
func registerKind(kind resourceKind) {
	if kind.NodeType == "" {
		panic(fmt.Sprintf("resource kind for service %q declares no node type", kind.Service))
	}
	if !kind.NodeType.IsKnown() {
		panic(fmt.Sprintf("resource kind for service %q declares unknown node type %q", kind.Service, kind.NodeType))
	}
	if kind.Suppress && kind.Node != nil {
		panic(fmt.Sprintf("resource kind for service %q is both suppressed and has its own builder", kind.Service))
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

// determineNodeType determines the knowledge graph node type from an AWS
// resource type and service name.
func (s *AWSSource) determineNodeType(resourceType, serviceName string) core.NodeType {
	return kindFor(resourceType, serviceName).NodeType
}

// determineSpecificType resolves the concrete cloud label for a resource,
// defaulting to the ontology node type's name when the resource declares no
// label of its own (so it is never blank).
func (s *AWSSource) determineSpecificType(resourceType, serviceName string, nodeType core.NodeType) string {
	if label := kindFor(resourceType, serviceName).SpecificType; label != "" {
		return label
	}
	return string(nodeType)
}
