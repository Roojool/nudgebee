package aws

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// Concrete schemas for AWS specific_types that have no dedicated source module or
// per-type extractor: their kinds (see kinds.go, declared just below) claim a
// whole service and land on a generic NodeType with base + common properties
// only. Each still needs a registered SpecificTypeSchema so the coverage guard and
// query_attributes wiring have an entry; the per-NodeType QueryablePropertiesMap
// fallback continues to drive their filtering.

// secretsManagerSecretSchema — Secrets Manager secrets (NodeTypeSecretVault).
// No resource-specific fields are lifted.
var secretsManagerSecretSchema = core.SpecificTypeSchema{
	SpecificType: "SecretsManagerSecret",
	NodeType:     core.NodeTypeSecretVault,
	Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "description"},
		{Name: "rotation_enabled"},
		{Name: "rotation_lambda_arn"},
		{Name: "rotation_rules_automatically_after_days"},
		{Name: "created_date"},
		{Name: "last_rotated_date"},
		{Name: "last_changed_date"},
		{Name: "last_accessed_date"},
		{Name: "deleted_date"},
		{Name: "kms_key_id"},
		{Name: "owning_service"},
		{Name: "primary_region"},
	},
}

// The following roll up to the generic NodeTypeCloudResource ontology. They carry
// only base properties (type/region/state), so no resource-specific PropertyDefs.
var acmCertificateSchema = core.SpecificTypeSchema{
	SpecificType: "ACMCertificate",
	NodeType:     core.NodeTypeCloudResource,
	Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "domain_name"},
		{Name: "key_algorithm"},
		{Name: "signature_algorithm"},
		{Name: "not_before"},
		{Name: "not_after"},
		{Name: "in_use_by"},
	},
}

var sageMakerResourceSchema = core.SpecificTypeSchema{
	SpecificType: "SageMakerResource",
	NodeType:     core.NodeTypeCloudResource,
	Properties:   []core.PropertyDef{},
}

var kinesisFirehoseStreamSchema = core.SpecificTypeSchema{
	SpecificType: "KinesisFirehoseStream",
	NodeType:     core.NodeTypeCloudResource,
	Properties:   []core.PropertyDef{},
}

var xRayResourceSchema = core.SpecificTypeSchema{
	SpecificType: "XRayResource",
	NodeType:     core.NodeTypeCloudResource,
	Properties:   []core.PropertyDef{},
}

func init() {
	core.RegisterSpecificTypeSchema(secretsManagerSecretSchema)
	core.RegisterSpecificTypeSchema(acmCertificateSchema)
	core.RegisterSpecificTypeSchema(sageMakerResourceSchema)
	core.RegisterSpecificTypeSchema(kinesisFirehoseStreamSchema)
	core.RegisterSpecificTypeSchema(xRayResourceSchema)
}

// The inventory rows this file's resources claim: what each row's node
// type, concrete label and metadata extraction are (see kinds.go).
func init() {
	secretsManagerRows := resourceKind{
		Service:      "AWSSecretsManager",
		NodeType:     core.NodeTypeSecretVault,
		SpecificType: "SecretsManagerSecret",
	}
	registerKind(resourceKind{
		Service:  "AmazonDataZone",
		NodeType: core.NodeTypeCloudResource,
	})
	registerKind(resourceKind{
		Service:  "AmazonBedrock",
		NodeType: core.NodeTypeAIService,
	})
	registerKind(resourceKind{
		Service:  "AWSEvents",
		NodeType: core.NodeTypeCloudResource,
	})
	registerKind(resourceKind{
		Service:  "AmazonQuickSight",
		NodeType: core.NodeTypeCloudResource,
	})
	registerKind(resourceKind{
		Service:  "AmazonCognito",
		NodeType: core.NodeTypeCloudResource,
	})
	registerKind(resourceKind{
		Service:  "AmazonAthena",
		NodeType: core.NodeTypeCloudResource,
	})
	registerKind(resourceKind{
		Service:  "awswaf",
		NodeType: core.NodeTypeCloudResource,
	})
	registerKind(resourceKind{
		Service:      "ACM",
		NodeType:     core.NodeTypeCloudResource,
		SpecificType: "ACMCertificate",
	})
	registerKind(resourceKind{
		Service:      "AmazonKinesisFirehose",
		NodeType:     core.NodeTypeCloudResource,
		SpecificType: "KinesisFirehoseStream",
	})
	registerKind(resourceKind{
		Service:      "AmazonSageMaker",
		NodeType:     core.NodeTypeCloudResource,
		SpecificType: "SageMakerResource",
	})
	registerKind(secretsManagerRows)
	registerKind(secretsManagerRows.withTypes("secret"))
	registerKind(resourceKind{
		Service:      "AWSXRay",
		NodeType:     core.NodeTypeCloudResource,
		SpecificType: "XRayResource",
	})
}

// Per-resource updates (see patch.go). These catch-all types only own a
// HOSTED_ON VPC edge.
func init() {
	registerPatchSpec(defaultVPCEdgeSpec(sources.PatchSpec{
		SpecificTypes: []string{"SecretsManagerSecret", "ACMCertificate", "KinesisFirehoseStream"},
		Creatable:     true,
	}))
}
