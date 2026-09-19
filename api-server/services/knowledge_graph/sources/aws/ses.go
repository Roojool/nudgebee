package aws

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
	"strings"
)

// sesConfigurationSetSchema / sesIdentitySchema — concrete schemas for SES resources
// (NodeTypeEmailService). state is a base property and the
// notification/event fields are read from cached meta during edge building.
var sesConfigurationSetSchema = core.SpecificTypeSchema{
	SpecificType: "SESConfigurationSet",
	NodeType:     core.NodeTypeEmailService,
	Properties:   []core.PropertyDef{},
}

var sesIdentitySchema = core.SpecificTypeSchema{
	SpecificType: "SESIdentity",
	NodeType:     core.NodeTypeEmailService,
	Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "identity_name"},
		{Name: "identity_type"},
		{Name: "sending_enabled"},
		{Name: "verification_status"},
		{Name: "dkim_signing_enabled"},
		{Name: "dkim_status"},
	},
}

func init() {
	core.RegisterSpecificTypeSchema(sesConfigurationSetSchema)
	core.RegisterSpecificTypeSchema(sesIdentitySchema)
}

// createSESEdges creates edges from SES resources to SNS topics for notifications
// EmailService → Topic (PUBLISHES_TO) for bounce/delivery/complaint notifications
func (s *AWSSource) createSESEdges(nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) []*core.DbEdge {
	edges := make([]*core.DbEdge, 0)

	for _, node := range nodes {
		meta, ok := getMetadataMap(node)
		if !ok {
			continue
		}

		// For identities: check notification topics (BounceTopic, DeliveryTopic, ComplaintTopic)
		if notificationAttrs, ok := meta["NotificationAttributes"].(map[string]interface{}); ok {
			for _, topicKey := range []string{"BounceTopic", "DeliveryTopic", "ComplaintTopic"} {
				if topicArn, ok := notificationAttrs[topicKey].(string); ok && topicArn != "" {
					if snsNode, exists := lookup.ByARN[topicArn]; exists {
						edges = append(edges, s.createEdge(node, snsNode, core.RelationshipPublishesTo,
							map[string]interface{}{
								"connection_type": "ses_notification",
								"event_type":      strings.ToLower(strings.TrimSuffix(topicKey, "Topic")),
							}, req))
					}
				}
			}
		}

		// For configuration-sets: check EventDestinations
		if eventDests, ok := meta["EventDestinations"].([]interface{}); ok {
			for _, dest := range eventDests {
				if destMap, ok := dest.(map[string]interface{}); ok {
					// Check for SNS destination
					if snsDestination, ok := destMap["SNSDestination"].(map[string]interface{}); ok {
						if topicArn, ok := snsDestination["TopicARN"].(string); ok && topicArn != "" {
							if snsNode, exists := lookup.ByARN[topicArn]; exists {
								edges = append(edges, s.createEdge(node, snsNode, core.RelationshipPublishesTo,
									map[string]interface{}{
										"connection_type": "ses_event_destination",
									}, req))
							}
						}
					}
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
		Service:  "AmazonSES",
		NodeType: core.NodeTypeEmailService,
	})
	registerKind(resourceKind{
		Service:      "AmazonSES",
		Types:        []string{"configuration-set"},
		NodeType:     core.NodeTypeEmailService,
		SpecificType: "SESConfigurationSet",
	})
	registerKind(resourceKind{
		Service:      "AmazonSES",
		Types:        []string{"identity"},
		NodeType:     core.NodeTypeEmailService,
		SpecificType: "SESIdentity",
	})
}

// The edges the account build asks this file's resources for (see dispatch.go).
func init() {
	// SES resources publish delivery notifications to SNS topics.
	registerEdgeStep(edgeStep{NodeType: core.NodeTypeEmailService, Name: "ses",
		Build: func(s *AWSSource, reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
			return s.createSESEdges(nodes, lookup, req), nil
		}})
}

// Per-resource updates (see patch.go). SES resources have no edges of their own.
func init() {
	registerPatchSpec(sources.PatchSpec{
		SpecificTypes: []string{"SESConfigurationSet", "SESIdentity"},
		Creatable:     true,
	}, nil)
}
