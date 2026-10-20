package azure

import (
	"encoding/json"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"strings"
)

// shouldSuppressAzureResource drops rows that are cost/billing constructs rather
// than deployed infrastructure. It is deliberately narrow: Azure billing-export
// rows (meta.nb_source="billing") include *real* resources that have no ARM
// counterpart (e.g. Private Endpoints), so we must NOT blanket-suppress by source.
// Reservation orders (Microsoft.Capacity/reservationOrders, type "reservationorders")
// are purchase commitments — they were surfacing as orphaned CloudResource phantoms.
func shouldSuppressAzureResource(r *sources.CloudResourceRow) bool {
	if r == nil {
		return true
	}
	if strings.EqualFold(r.Type, "reservationorders") {
		return true
	}
	if strings.Contains(strings.ToLower(r.ServiceName), "reservationorders") ||
		strings.Contains(strings.ToLower(r.ServiceName), "microsoft.capacity") {
		return true
	}
	return false
}

// createNodeFromResource creates a knowledge graph node from an Azure cloud resource
func (s *AzureSource) createNodeFromResource(resource *sources.CloudResourceRow, req *core.SourceBuildRequest) *core.DbNode {
	source := "azure"
	kind := kindFor(resource.Type, resource.ServiceName)
	nodeType := kind.NodeType

	properties := make(map[string]interface{})
	properties["name"] = resource.Name
	properties["type"] = resource.Type
	properties["status"] = resource.Status
	properties["cloud_provider"] = resource.CloudProvider
	properties["region"] = resource.Region
	properties["arn"] = resource.ARN // Azure Resource ID stored in arn column
	properties["resource_id"] = resource.ResourceID
	properties["service_name"] = resource.ServiceName
	properties["is_active"] = resource.IsActive
	properties["external_resource_id"] = resource.ExternalResourceID
	properties["labels"] = resource.Tags

	// Store identifiers
	properties["nb_resource_id"] = resource.ID
	properties["nb_account_id"] = resource.Account
	properties["account_number"] = resource.AccountNumber // Azure subscription ID

	// Add subtype
	properties["subtype"] = resource.Type

	// Declare the concrete cloud label (specific_type). NewNode lifts this out of
	// properties into the dedicated column.
	properties["specific_type"] = kind.specificType(resource)

	// Parse metadata
	if len(resource.Meta) > 0 && string(resource.Meta) != "{}" {
		var metaMap map[string]interface{}
		if err := json.Unmarshal(resource.Meta, &metaMap); err == nil {
			s.extractMetadataForKind(properties, metaMap, kind)
		}
		// Resources whose children are embedded in their metadata keep it, so
		// the pass that extracts them can read it back (a VNet's subnets).
		if kind.KeepRawMeta {
			properties["_raw_meta"] = resource.Meta
		}
	}

	// Parse tags
	if len(resource.Tags) > 0 && string(resource.Tags) != "{}" {
		var tagsMap map[string]interface{}
		if err := json.Unmarshal(resource.Tags, &tagsMap); err == nil {
			properties["labels"] = tagsMap
		}
	}

	// Build unique key
	tempNode := &core.DbNode{
		NodeType:       nodeType,
		Properties:     properties,
		CloudAccountID: req.CloudAccountID,
	}
	uniqueKey := s.GenerateUniqueKey(tempNode)

	return core.NewNode(nodeType, uniqueKey, properties, req.TenantID, req.CloudAccountID, source)
}

// extractServicePrefix extracts the provider prefix from a full service_name path.
// e.g., "microsoft.compute/virtualmachines" → "microsoft.compute"
func extractServicePrefix(serviceName string) string {
	if idx := strings.Index(serviceName, "/"); idx > 0 {
		return serviceName[:idx]
	}
	return serviceName
}

// extractMetadataForKind extracts the metadata fields a node keeps: the ones
// common to every Azure resource, then the ones the resource's own extractor
// pulls out of the Resource Graph "properties" object.
// Azure Resource Graph returns meta in a nested format:
//
//	{ "id": "...", "name": "...", "properties": { ...actual resource data... } }
//
// This method normalizes by checking both top-level and nested "properties" keys.
func (s *AzureSource) extractMetadataForKind(properties map[string]interface{}, metaMap map[string]interface{}, kind resourceKind) {
	// Azure Resource Graph nests actual data under "properties" key
	propsMap := metaMap
	if nested, ok := metaMap["properties"].(map[string]interface{}); ok {
		propsMap = nested
	}

	// Extract resource group from top-level meta (Azure Resource Graph puts it at top level)
	if rg, ok := metaMap["resourceGroup"].(string); ok && rg != "" {
		properties["resource_group"] = rg
	}

	// Extract location from top-level meta
	if loc, ok := metaMap["location"].(string); ok && loc != "" {
		// Override region with the location from meta if present (more accurate)
		properties["region"] = loc
	}

	// Extract VNet/Subnet IDs common to many resource types (check both levels)
	s.extractCommonNetworkIDs(properties, metaMap, propsMap)

	if kind.Extract != nil {
		kind.Extract(s, properties, propsMap)
	}
}
