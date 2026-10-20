package gcp

import (
	"encoding/json"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"strings"
)

// Concrete per-specific_type property schemas for the GCP specific_types that
// are produced entirely by the shared createNodeFromResource path below — they
// have no per-type extractor and carry only universal base keys (name/region/
// subtype/status/labels/…) plus the raw `meta` blob. They are declared here,
// createNodeFromResource creates a knowledge graph node from a GCP resource row.
// Returns nil when the row should be suppressed (e.g. NIC / vm-manager noise
// via shouldSuppressGCPResource, or rows redirected to a typed node such as
// IAM ServiceAccount → ServiceIdentity below); callers must skip nil.
func (s *GCPSource) createNodeFromResource(resource *sources.CloudResourceRow, req *core.SourceBuildRequest) *core.DbNode {
	if shouldSuppressGCPResource(resource) {
		return nil
	}

	source := "gcp"
	kind := kindFor(resource.Type, resource.ServiceName)
	nodeType := kind.NodeType

	// A resource can declare that its rows build a different kind of node
	// altogether (IAM service accounts become typed identities).
	if kind.Node != nil {
		return kind.Node(s, resource, req)
	}

	properties := make(map[string]interface{})
	properties["name"] = resource.Name
	properties["type"] = resource.Type
	properties["status"] = resource.Status
	properties["cloud_provider"] = resource.CloudProvider
	properties["region"] = resource.Region
	properties["labels"] = resource.Tags
	properties["arn"] = resource.ARN
	properties["resource_id"] = resource.ResourceID
	properties["service_name"] = resource.ServiceName
	properties["is_active"] = resource.IsActive
	properties["external_resource_id"] = resource.ExternalResourceID

	// Store identifiers
	properties["nb_resource_id"] = resource.ID
	properties["nb_account_id"] = resource.Account
	properties["account_number"] = resource.AccountNumber

	// GCP project ID == the cloud account's project, carried as account_number.
	// Prefer it over parsing resource.Name: most GCP resources have a bare Name
	// (e.g. "default", "backendgaeservice"), so extractGCPProjectID(Name) wrongly
	// yields the resource's own name as the project — corrupting gcp_project_id on
	// nearly every node and any DNS synthesized from it (synthesizeGCPEndpointDNS).
	projectID := resource.AccountNumber
	if projectID == "" {
		projectID = extractGCPProjectID(resource.Name)
	}
	if projectID != "" {
		properties["gcp_project_id"] = projectID
	}

	// The concrete flavour of a shared ontology type (Cloud SQL vs BigQuery
	// under Database, Cloud Storage vs Filestore vs persistent disks under
	// Storage). Resources that declare none are described by their row type.
	if kind.Subtype != "" {
		properties["subtype"] = kind.Subtype
	} else if _, exists := properties["subtype"]; !exists {
		properties["subtype"] = resource.Type
	}

	// Declare the concrete cloud label (specific_type). NewNode lifts this out of
	// properties into the dedicated column.
	properties["specific_type"] = kind.specificType()

	// Parse metadata if available and extract essential fields
	if len(resource.Meta) > 0 && string(resource.Meta) != "{}" {
		var metaMap map[string]interface{}
		if err := json.Unmarshal(resource.Meta, &metaMap); err == nil && len(metaMap) > 0 {
			properties["meta"] = metaMap
			if kind.Extract != nil {
				kind.Extract(s, properties, metaMap)
			}
		}
	}

	// Synthesize public DNS for GCP resources whose metadata doesn't expose
	// one (Cloud Storage today). Runs unconditionally — even for rows with
	// empty `meta` — since the synthesizer reads only `name` + `service_name`
	// + `region`, all set above. No-op when dns_name is already populated by
	// Cloud SQL connectionName / GKE endpoint / Cloud Run url extractors.
	synthesizeGCPEndpointDNS(properties)

	// Parse and add tags (normalize array values to strings for GCP Asset Inventory format)
	if len(resource.Tags) > 0 && string(resource.Tags) != "{}" {
		var tagsMap map[string]interface{}
		if err := json.Unmarshal(resource.Tags, &tagsMap); err == nil {
			normalizedLabels := normalizeGCPLabels(tagsMap)
			properties["labels"] = normalizedLabels

			if kind.Labels != nil {
				kind.Labels(s, properties, normalizedLabels)
			}
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

// shouldSuppressGCPResource returns true for cloud_resourses rows that should
// NEVER produce a KG node. Two categories today (issue #31101 gaps #8/#9):
//
//  1. NetworkInterface rows: GCP names every primary NIC `nic0` and our
//     unique-key derivation has no per-instance discriminator, so 16 distinct
//     NICs collapse to 1 KG node every build. The data they carry
//     (internal_ip, external_ip, subnet, network) is already on the parent
//     ComputeInstance node — mirroring AWS, which doesn't emit ENIs as
//     first-class KG nodes either.
//
//  2. `vm-manager` billing stubs: GCP billing export emits a single placeholder
//     row per project for the VM Manager service. It's not a listable resource
//     (the lister returns ErrUnsupported); the only `cloud_resourses` row for
//     it has `meta.nb_source="billing"` and a project-ID name. Zero query
//     value as a CloudResource node.
func shouldSuppressGCPResource(r *sources.CloudResourceRow) bool {
	if r == nil {
		// Future refactors / tests could call this with a nil pointer; the
		// current caller always passes non-nil but the guard is cheap.
		// Suppressing nil is the safer default.
		return true
	}
	if strings.EqualFold(r.ServiceName, "compute.googleapis.com/NetworkInterface") {
		return true
	}
	if strings.EqualFold(r.Type, "vm-manager") {
		return true
	}
	// IAM policy binding rows feed HAS_ACCESS_TO / PUBLISHES_TO / SUBSCRIBES_TO
	// edges (createHasAccessEdges); they are relationship data, not graph nodes.
	if strings.EqualFold(r.ServiceName, gcpIAMPolicyServiceName) || strings.EqualFold(r.Type, gcpIAMBindingType) {
		return true
	}
	// BigQuery dataset/table/view rows are high-cardinality data-tier leaf nodes —
	// the collector emits one row per table and a single project can hold tens of
	// thousands, swamping the graph with nodes that carry no topology value — so
	// these three resource types are excluded from the KG. Only the dataset/table/view
	// leaves are dropped; the BigQuery service is otherwise untouched (MaterializedView
	// and ExternalTable are already excluded upstream by GCPDefaultServiceTypeFilter).
	// This gate is the single on/off switch — the BigQuery type mappings, hierarchy
	// edge builder (createBigQueryEdges) and IAM dataset target are deliberately left
	// in place (inert while this gate drops the rows) so re-enabling is a one-line
	// removal here.
	switch strings.ToLower(r.Type) {
	case "bigquery.googleapis.com/dataset",
		"bigquery.googleapis.com/table",
		"bigquery.googleapis.com/view":
		return true
	}
	// GCP billing-export rollup rows (meta.nb_source="billing") are cost-reporting
	// aggregates — one per project per billed service, always named after the project
	// — not real resources. They carry no topology data and would otherwise become
	// orphaned nodes named after the project: phantom ComputeInstance (compute-engine),
	// and generic CloudResource / LogAggregator / MonitoringService for Cloud Tasks,
	// Secret Manager, KMS, Maps/Time-Zone APIs, Cloud Logging/Monitoring/Trace, etc.
	// Real resources always arrive via nb_source "api"/"asset" with their own names
	// and metadata, so they are unaffected. (Subsumes the vm-manager billing stub.)
	if gcpResourceMetaSource(r) == "billing" {
		return true
	}
	return false
}

// gcpResourceMetaSource returns the meta.nb_source marker ("billing", "api",
// "asset", …) for a cloud_resourses row, or "" when absent/unparseable.
func gcpResourceMetaSource(r *sources.CloudResourceRow) string {
	if r == nil || len(r.Meta) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(r.Meta, &m); err != nil {
		return ""
	}
	source, _ := m["nb_source"].(string)
	return source
}

// Per-resource updates (see patch.go). Buckets, secrets and registries have no
// edges of their own: access grants, secret references and image pulls point at
// them from other rows.
func init() {
	registerPatchSpec(sources.PatchSpec{
		SpecificTypes: []string{"GCSBucket", "SecretManagerSecret", "ArtifactRegistry"},
		Creatable:     true,
	}, nil)
}
