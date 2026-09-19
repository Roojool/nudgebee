package aws

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
)

// securityHubSchema / securityHubStandardSchema / guardDutyDetectorSchema — concrete
// schemas for the AWS security-posture services (NodeTypeSecurityService). type/state
// are base properties; standards_arn / hub_arn are hoisted via the per-NodeType
// QueryablePropertiesMap fallback.
var securityHubSchema = core.SpecificTypeSchema{
	SpecificType: "SecurityHub",
	NodeType:     core.NodeTypeSecurityService,
	Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "subscribed_at"},
		{Name: "auto_enable_controls"},
	},
}

var securityHubStandardSchema = core.SpecificTypeSchema{
	SpecificType: "SecurityHubStandard",
	NodeType:     core.NodeTypeSecurityService,
	Properties:   []core.PropertyDef{},
}

var guardDutyDetectorSchema = core.SpecificTypeSchema{
	SpecificType: "GuardDutyDetector",
	NodeType:     core.NodeTypeSecurityService,
	Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "account_id"},
		{Name: "finding_publishing_frequency"},
		{Name: "service_role"},
		{Name: "created_at"},
		{Name: "updated_at"},
	},
}

func init() {
	core.RegisterSpecificTypeSchema(securityHubSchema)
	core.RegisterSpecificTypeSchema(securityHubStandardSchema)
	core.RegisterSpecificTypeSchema(guardDutyDetectorSchema)
}

// createSecurityHubEdges creates edges from SecurityHub standards to hub
// SecurityService (standard) → SecurityService (hub) (BELONGS_TO)
func (s *AWSSource) createSecurityHubEdges(nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) []*core.DbEdge {
	edges := make([]*core.DbEdge, 0)

	// Find the hub node(s) for this account
	hubNodes := make([]*core.DbNode, 0)
	for _, node := range lookup.ByNodeType[core.NodeTypeSecurityService] {
		if serviceName, ok := node.Properties["service_name"].(string); ok && serviceName == "AWSSecurityHub" {
			if resourceType, ok := node.Properties["type"].(string); ok && resourceType == "hub" {
				hubNodes = append(hubNodes, node)
			}
		}
	}

	// Create edges from standards to their hub
	for _, node := range nodes {
		if resourceType, ok := node.Properties["type"].(string); ok && resourceType == "standard" {
			for _, hubNode := range hubNodes {
				// Match by account
				nodeAccount, _ := node.Properties["nb_account_id"].(string)
				hubAccount, _ := hubNode.Properties["nb_account_id"].(string)
				if nodeAccount == hubAccount {
					edges = append(edges, s.createEdge(node, hubNode, core.RelationshipBelongsTo,
						map[string]interface{}{
							"connection_type": "security_standard",
						}, req))
				}
			}
		}
	}

	return edges
}

// The inventory rows this file's resources claim: what each row's node
// type, concrete label and metadata extraction are (see kinds.go).
func init() {
	registerKind(resourceKind{
		Service:  "AWSSecurityHub",
		NodeType: core.NodeTypeSecurityService,
	})
	registerKind(resourceKind{
		Service:      "AmazonGuardDuty",
		NodeType:     core.NodeTypeSecurityService,
		SpecificType: "GuardDutyDetector",
	})
	registerKind(resourceKind{
		Service:      "AWSSecurityHub",
		Types:        []string{"hub"},
		NodeType:     core.NodeTypeSecurityService,
		SpecificType: "SecurityHub",
	})
	registerKind(resourceKind{
		Service:      "AWSSecurityHub",
		Types:        []string{"standard"},
		NodeType:     core.NodeTypeSecurityService,
		SpecificType: "SecurityHubStandard",
	})
}

// The edges the account build asks this file's resources for (see dispatch.go).
func init() {
	// SecurityHub standards belong to the hub.
	registerEdgeStep(edgeStep{NodeType: core.NodeTypeSecurityService, Name: "securityhub",
		Build: func(s *AWSSource, reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
			return s.createSecurityHubEdges(nodes, lookup, req), nil
		}})
}

// Per-resource updates (see patch.go). Security Hub standards own their
// BELONGS_TO edge to the hub, resolved against the account's other security
// services.
func init() {
	registerPatchSpec(sources.PatchSpec{
		SpecificTypes: []string{"SecurityHub", "SecurityHubStandard", "GuardDutyDetector"},
		Creatable:     true,
		Context:       []core.NodeType{core.NodeTypeSecurityService},
		Signature:     signature(edgeKind(core.RelationshipBelongsTo, "security_standard")),
	}, func(s *AWSSource, node *core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
		return s.createSecurityHubEdges([]*core.DbNode{node}, lookup, req), nil
	})
}
