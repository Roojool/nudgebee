package aws

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
	"strings"
)

// ecsClusterSchema — concrete schema for ECS clusters (NodeTypeManagedCluster).
// ECS clusters flow through extractClusterMetadata (see eks.go) just like EKS.
var ecsClusterSchema = core.SpecificTypeSchema{
	SpecificType: "ECSCluster",
	NodeType:     core.NodeTypeManagedCluster,
	Properties: []core.PropertyDef{
		{Name: "cluster_version", Indexed: true},
		{Name: "cluster_endpoint", Indexed: true},
		{Name: "cluster_status", Indexed: true},
		{Name: "vpc_id", Indexed: true},
		{Name: "dns_name", Indexed: true},
		{Name: "role_arn"},
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "cluster_name"},
		{Name: "ecc_kms_key_id"},
		{Name: "ecc_logging"},
		{Name: "ecc_log_configuration_cloud_watch_log_group_name"},
		{Name: "ecc_log_configuration_cloud_watch_encryption_enabled"},
		{Name: "ecc_log_configuration_s3_bucket_name"},
		{Name: "ecc_log_configuration_s3_encryption_enabled"},
		{Name: "ecc_log_configuration_s3_key_prefix"},
		{Name: "capacity_providers"},
		{Name: "attachments_status"},
	},
}

// ecsServiceSchema — ECS services (NodeTypeWorkload). namespace/cluster/replicas are
// resolved via edges, not lifted into properties at creation.
var ecsServiceSchema = core.SpecificTypeSchema{
	SpecificType: "ECSService",
	NodeType:     core.NodeTypeWorkload,
	Properties: []core.PropertyDef{
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "cluster_arn"},
		{Name: "desired_count"},
		{Name: "running_count"},
		{Name: "pending_count"},
		{Name: "launch_type"},
		{Name: "platform_version"},
		{Name: "platform_family"},
		{Name: "task_definition"},
		{Name: "deployment_config_circuit_breaker_enable"},
		{Name: "deployment_config_circuit_breaker_rollback"},
		{Name: "deployment_config_maximum_percent"},
		{Name: "deployment_config_minimum_healthy_percent"},
		{Name: "created_at"},
		{Name: "health_check_grace_period_seconds"},
		{Name: "created_by"},
		{Name: "enable_ecs_managed_tags"},
		{Name: "propagate_tags"},
		{Name: "enable_execute_command"},
	},
}

func init() {
	core.RegisterSpecificTypeSchema(ecsClusterSchema)
	core.RegisterSpecificTypeSchema(ecsServiceSchema)
}

// createECSServiceEdges creates edges for ECS Services connecting to ECS Clusters
// ECS Service ARN format: arn:aws:ecs:{region}:{account}:service/{cluster-name}/{service-name}
func (s *AWSSource) createECSServiceEdges(nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) []*core.DbEdge {
	edges := make([]*core.DbEdge, 0)

	for _, node := range nodes {
		// Only process ECS services (service_name = AmazonECS)
		serviceName, ok := getStringProperty(node, "service_name")
		if !ok || serviceName != "AmazonECS" {
			continue
		}

		// Get the ARN to extract cluster name
		// ECS Service ARN format: arn:aws:ecs:{region}:{account}:service/{cluster-name}/{service-name}
		arn, ok := getStringProperty(node, "arn")
		if !ok || arn == "" {
			// Try external_resource_id as fallback
			arn, ok = getStringProperty(node, "external_resource_id")
			if !ok || arn == "" {
				continue
			}
		}

		// Parse cluster name from ARN
		// Example: arn:aws:ecs:<region>:<aws-account-id>:service/<cluster-name>/<service-name>
		clusterName := extractECSClusterNameFromServiceARN(arn)
		if clusterName == "" {
			continue
		}

		clusterFound := false
		for _, clusterNode := range lookup.GetNodesByTypeAndName(core.NodeTypeManagedCluster, clusterName) {
			if svcName, ok := getStringProperty(clusterNode, "service_name"); !ok || svcName != "AmazonECS" {
				continue
			}
			clusterFound = true
			edges = append(edges, s.createEdge(node, clusterNode, core.RelationshipRunsIn,
				map[string]interface{}{
					"connection_type": "ecs_cluster",
					"cluster_name":    clusterName,
				}, req))
			break
		}
		if !clusterFound {
			s.logger.Warn("ECS service cluster node not found; edge skipped",
				"service_name", node.Properties["name"],
				"cluster_name", clusterName,
				"arn", arn)
		}
	}

	return edges
}

// extractECSClusterNameFromServiceARN extracts the cluster name from an ECS service ARN
// ARN formats:
//   - With explicit cluster: arn:aws:ecs:{region}:{account}:service/{cluster-name}/{service-name}
//   - Default cluster: arn:aws:ecs:{region}:{account}:service/{service-name}
func extractECSClusterNameFromServiceARN(arn string) string {
	// Look for the pattern "service/{cluster-name}/{service-name}" or "service/{service-name}"
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return ""
	}

	// The last part contains "service/{cluster-name}/{service-name}" or "service/{service-name}"
	resourcePart := parts[len(parts)-1]
	if !strings.HasPrefix(resourcePart, "service/") {
		return ""
	}

	// Remove "service/" prefix and split by "/"
	resourcePath := strings.TrimPrefix(resourcePart, "service/")
	pathParts := strings.Split(resourcePath, "/")

	// If only one part, it's the service name and the cluster is "default"
	if len(pathParts) == 1 {
		return "default"
	}

	// First part is the cluster name
	return pathParts[0]
}

// The inventory rows this file's resources claim: what each row's node
// type, concrete label and metadata extraction are (see kinds.go).
func init() {
	registerKind(resourceKind{
		Service:      "AmazonECS",
		NodeType:     core.NodeTypeCloudResource,
		SpecificType: "ECSCluster",
	})
	registerKind(resourceKind{
		Service:      "AmazonECS",
		Types:        []string{"cluster"},
		NodeType:     core.NodeTypeManagedCluster,
		SpecificType: "ECSCluster",
		Extract:      (*AWSSource).extractClusterMetadata,
	})
	registerKind(resourceKind{
		Service:      "AmazonECS",
		Types:        []string{"service"},
		NodeType:     core.NodeTypeWorkload,
		SpecificType: "ECSService",
	})
}

// The edges the account build asks this file's resources for (see dispatch.go).
func init() {
	// ECS services run in their cluster.
	registerEdgeStep(edgeStep{NodeType: core.NodeTypeWorkload, Name: "ecs_service",
		Build: func(s *AWSSource, reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
			return s.createECSServiceEdges(nodes, lookup, req), nil
		}})
}

// Per-resource updates (see patch.go). A service owns its RUNS_IN edge to its
// cluster; both services and clusters can be created from their rows.
func init() {
	registerPatchSpec(sources.PatchSpec{
		SpecificTypes: []string{"ECSService"},
		Creatable:     true,
		Context:       []core.NodeType{core.NodeTypeManagedCluster},
		Signature:     signature(edgeKind(core.RelationshipRunsIn, "ecs_cluster")),
	}, func(s *AWSSource, node *core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
		return s.createECSServiceEdges([]*core.DbNode{node}, lookup, req), nil
	})

	registerPatchSpec(sources.PatchSpec{
		SpecificTypes: []string{"ECSCluster"},
		Creatable:     true,
	}, nil)
}
