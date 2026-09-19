package gcp

import (
	"nudgebee/services/knowledge_graph/core"
)

// GCP services the graph records but has no resource file for: the collector
// reports one row per project (or a bare catalog entry) with no metadata to
// extract and no edges of its own, so there is nothing to put in a file of
// their own beyond what they are. Resources with real behaviour live in their
// own file — compute.go, cloudsql.go, gke.go and so on.

// co-located with the builder that writes their properties, so every concrete
// label the source can emit (see kinds.go) has a registered schema even when
// it exposes no resource-specific, filterable field. GCSBucket additionally
// carries a synthesized public dns_name (declared, not Indexed — it is already
// part of every GCS node in existing goldens).
var (
	gcpDatabaseSchema       = core.SpecificTypeSchema{SpecificType: "GCPDatabase", NodeType: core.NodeTypeDatabase}
	gcpStorageSchema        = core.SpecificTypeSchema{SpecificType: "GCPStorage", NodeType: core.NodeTypeStorage}
	filestoreInstanceSchema = core.SpecificTypeSchema{SpecificType: "FilestoreInstance", NodeType: core.NodeTypeStorage}
	gcsBucketSchema         = core.SpecificTypeSchema{SpecificType: "GCSBucket", NodeType: core.NodeTypeStorage, Properties: []core.PropertyDef{
		{Name: "dns_name"},
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "bucket_id"},
		{Name: "project_number"},
		{Name: "self_link"},
		{Name: "kind"},
		{Name: "location_type"},
		{Name: "meta_generation"},
		{Name: "storage_class"},
		{Name: "time_created"},
		{Name: "retention_period"},
		{Name: "iam_config_bucket_policy_only"},
		{Name: "iam_config_public_access_prevention"},
		{Name: "owner_entity"},
		{Name: "owner_entity_id"},
		{Name: "versioning_enabled"},
		{Name: "log_bucket"},
		{Name: "requester_pays"},
		{Name: "default_kms_key_name"},
		{Name: "acl_public"},
	}}
	artifactRegistrySchema = core.SpecificTypeSchema{SpecificType: "ArtifactRegistry", NodeType: core.NodeTypeContainerRegistry, Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "format"},
		{Name: "mode"},
		{Name: "description"},
		{Name: "registry_uri"},
		{Name: "size_bytes"},
		{Name: "kms_key_name"},
		{Name: "create_time"},
		{Name: "update_time"},
		{Name: "cleanup_policy_dry_run"},
		{Name: "vulnerability_scanning_enabled"},
		{Name: "project_id"},
	}}
	cloudNATGatewaySchema     = core.SpecificTypeSchema{SpecificType: "CloudNATGateway", NodeType: core.NodeTypeNetworkGateway}
	gcpExternalIPSchema       = core.SpecificTypeSchema{SpecificType: "GCPExternalIP", NodeType: core.NodeTypePublicIP}
	cloudLoggingSchema        = core.SpecificTypeSchema{SpecificType: "CloudLogging", NodeType: core.NodeTypeLogAggregator}
	cloudMonitoringSchema     = core.SpecificTypeSchema{SpecificType: "CloudMonitoring", NodeType: core.NodeTypeMonitoringService}
	vertexAISchema            = core.SpecificTypeSchema{SpecificType: "VertexAI", NodeType: core.NodeTypeAIService}
	secretManagerSecretSchema = core.SpecificTypeSchema{SpecificType: "SecretManagerSecret", NodeType: core.NodeTypeSecretVault, Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "project_id"},
		{Name: "rotation_enabled"},
		{Name: "rotation_period"},
		{Name: "rotation_next_time"},
		{Name: "created_date"},
		{Name: "expire_time"},
		{Name: "replication_type"},
		{Name: "etag"},
		{Name: "topics"},
		{Name: "version_aliases"},
	}}
	cloudKMSKeySchema = core.SpecificTypeSchema{SpecificType: "CloudKMSKey", NodeType: core.NodeTypeEncryptionKey, Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "rotation_period"},
		{Name: "purpose"},
		{Name: "key_ring_id"},
	}}
)

func init() {
	for _, s := range []core.SpecificTypeSchema{
		gcpDatabaseSchema, gcpStorageSchema, filestoreInstanceSchema, gcsBucketSchema,
		artifactRegistrySchema, cloudNATGatewaySchema, gcpExternalIPSchema, cloudLoggingSchema,
		cloudMonitoringSchema, vertexAISchema, secretManagerSecretSchema, cloudKMSKeySchema,
	} {
		core.RegisterSpecificTypeSchema(s)
	}
}

// The inventory rows these services claim: what each row's node type, concrete
// label and subtype are (see kinds.go).
func init() {
	vmManagerRows := resourceKind{
		Service:  "VM Manager",
		NodeType: core.NodeTypeCloudResource,
	}
	artifactRegistryRows := resourceKind{
		Service:      "Artifact Registry",
		NodeType:     core.NodeTypeContainerRegistry,
		SpecificType: "ArtifactRegistry",
		Subtype:      "ArtifactRegistry",
	}
	cloudLoggingRows := resourceKind{
		Service:      "Cloud Logging",
		NodeType:     core.NodeTypeLogAggregator,
		SpecificType: "CloudLogging",
		Subtype:      "CloudLogging",
	}
	cloudMonitoringRows := resourceKind{
		Service:      "Cloud Monitoring",
		NodeType:     core.NodeTypeMonitoringService,
		SpecificType: "CloudMonitoring",
		Subtype:      "CloudMonitoring",
	}
	cloudFilestoreRows := resourceKind{
		Service:      "Cloud Filestore",
		NodeType:     core.NodeTypeStorage,
		SpecificType: "FilestoreInstance",
		Subtype:      "Filestore",
	}
	cloudStorageRows := resourceKind{
		Service:      "Cloud Storage",
		NodeType:     core.NodeTypeStorage,
		SpecificType: "GCSBucket",
		Subtype:      "CloudStorage",
	}
	vertexAiRows := resourceKind{
		Service:      "Vertex AI",
		NodeType:     core.NodeTypeAIService,
		SpecificType: "VertexAI",
		Subtype:      "Vertex AI",
	}
	geminiApiRows := resourceKind{
		Service:      "Gemini API",
		NodeType:     core.NodeTypeAIService,
		SpecificType: "VertexAI",
		Subtype:      "Gemini API",
	}
	claudeSonnet45Rows := resourceKind{
		Service:      "Claude Sonnet 4.5",
		NodeType:     core.NodeTypeAIService,
		SpecificType: "VertexAI",
		Subtype:      "Claude Sonnet 4.5",
	}
	registerKind(vmManagerRows)
	registerKind(resourceKind{
		Service:  "Cloud Tasks",
		NodeType: core.NodeTypeQueue,
	})
	registerKind(resourceKind{
		Service:  "Cloud Scheduler",
		NodeType: core.NodeTypeCronJob,
	})
	registerKind(vmManagerRows.withTypes("vm-manager"))
	registerKind(artifactRegistryRows)
	registerKind(artifactRegistryRows.withTypes("artifact-registry"))
	registerKind(cloudLoggingRows)
	registerKind(cloudLoggingRows.withTypes("cloud-logging"))
	registerKind(cloudMonitoringRows)
	registerKind(cloudMonitoringRows.withTypes("cloud-monitoring"))
	registerKind(cloudFilestoreRows)
	registerKind(cloudFilestoreRows.withTypes("cloud-filestore"))
	registerKind(resourceKind{
		Service:      "Firestore",
		NodeType:     core.NodeTypeDatabase,
		SpecificType: "GCPDatabase",
		Subtype:      "Database",
	})
	registerKind(cloudStorageRows)
	registerKind(cloudStorageRows.withTypes("storage.googleapis.com/bucket"))
	registerKind(resourceKind{
		Service:      "Secret Manager",
		NodeType:     core.NodeTypeSecretVault,
		SpecificType: "SecretManagerSecret",
	})
	registerKind(vertexAiRows)
	registerKind(geminiApiRows)
	registerKind(claudeSonnet45Rows)
	registerKind(claudeSonnet45Rows.withTypes("claude-sonnet-4.5"))
	registerKind(geminiApiRows.withTypes("gemini-api"))
	registerKind(vertexAiRows.withTypes("vertex-ai", "vertex-ai-model", "vertex-ai-endpoint"))
}
