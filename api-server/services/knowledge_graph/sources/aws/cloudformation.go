package aws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"nudgebee/services/cloud"
	"nudgebee/services/common"
	"nudgebee/services/config"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
	"strings"
)

// cloudFormationStackSchema — concrete schema for CloudFormation stacks
// (NodeTypeInfraStack). Fields written by
// extractCloudFormationMetadata; status is a base property.
var cloudFormationStackSchema = core.SpecificTypeSchema{
	SpecificType: "CloudFormationStack",
	NodeType:     core.NodeTypeInfraStack,
	Properties: []core.PropertyDef{
		{Name: "role_arn"},
		{Name: "parameters"},
		{Name: "outputs"},
		// Additional provider fields (not yet emitted by our extractor):
		{Name: "stack_name"},
		{Name: "description"},
		{Name: "stack_status"},
		{Name: "stack_status_reason"},
		{Name: "creation_time"},
		{Name: "last_updated_time"},
		{Name: "parent_id"},
		{Name: "root_id"},
		{Name: "disable_rollback"},
	},
}

func init() { core.RegisterSpecificTypeSchema(cloudFormationStackSchema) }

// extractCloudFormationMetadata extracts essential fields for CloudFormation stacks
func (s *AWSSource) extractCloudFormationMetadata(properties map[string]interface{}, metaMap map[string]interface{}) {
	// RoleARN (IAM role used by CloudFormation)
	if roleARN, ok := metaMap["RoleARN"].(string); ok && roleARN != "" {
		properties["role_arn"] = roleARN
	}

	// Parameters (stack input parameters)
	if params, ok := metaMap["Parameters"].([]interface{}); ok && len(params) > 0 {
		if paramsJSON, err := json.Marshal(params); err == nil {
			properties["parameters"] = string(paramsJSON)
		}
	}

	// Outputs (stack output values)
	if outputs, ok := metaMap["Outputs"].([]interface{}); ok && len(outputs) > 0 {
		if outputsJSON, err := json.Marshal(outputs); err == nil {
			properties["outputs"] = string(outputsJSON)
		}
	}
}

// buildCFNListStackResourcesCommand returns the CLI command listing a stack's resources.
// Stack names are region-scoped, so --region is always present: without it the AWS CLI
// resolves against the collector's AWS_DEFAULT_REGION and reports every stack in any
// other region as "does not exist" (#36573). Arguments must be validated by the caller.
func buildCFNListStackResourcesCommand(stackName, region string) string {
	return fmt.Sprintf("aws cloudformation list-stack-resources --stack-name %s --region %s --output json", stackName, region)
}

// cfnStackResourcesCacheKey keys the cfn_stack_resources cache. The region is part of
// the key because stack names are only unique within a region — without it two
// same-named stacks in different regions share one entry and serve each other's
// resource lists. It must stay in step with buildCFNListStackResourcesCommand.
func cfnStackResourcesCacheKey(accountID, region, stackName string) string {
	return fmt.Sprintf("%s:%s:%s", accountID, region, stackName)
}

// fetchCloudFormationStackResources fetches all resources managed by a CloudFormation stack.
// Results are cached for 2 hours to avoid redundant AWS API calls across graph builds.
func (s *AWSSource) fetchCloudFormationStackResources(reqCtx *security.RequestContext, accountID string, region string, stackName string) ([]CloudFormationStackResource, error) {
	// Validate inputs to prevent command injection
	if err := validateStackName(stackName); err != nil {
		return nil, fmt.Errorf("invalid stack name: %w", err)
	}
	// validateAWSRegion is shared and permits "" (meaning "use the default"); this
	// lookup does not, so require a region explicitly before validating its shape.
	if region == "" {
		return nil, fmt.Errorf("region is required to look up stack %s", stackName)
	}
	if err := validateAWSRegion(region); err != nil {
		return nil, fmt.Errorf("invalid region: %w", err)
	}

	// Check cache first
	cacheKey := cfnStackResourcesCacheKey(accountID, region, stackName)
	if cached, found := common.CacheGet("cfn_stack_resources", cacheKey); found {
		var resources []CloudFormationStackResource
		if unmarshalErr := json.Unmarshal(cached, &resources); unmarshalErr == nil {
			s.logger.Debug("CloudFormation stack resources cache hit",
				"stack_name", stackName,
				"account_id", accountID,
				"region", region,
				"resource_count", len(resources))
			return resources, nil
		}
	}

	// Arguments are safe after validation above
	cmd := buildCFNListStackResourcesCommand(stackName, region)

	s.logger.Debug("Fetching CloudFormation stack resources",
		"stack_name", stackName,
		"account_id", accountID,
		"region", region)

	// Execute AWS CLI command via cloud collector
	resp, err := cloud.ExecuteCli(reqCtx, cloud.CloudExecuteCliCommandRequest{
		AccountID: accountID,
		Command:   cmd,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch CloudFormation stack resources: %w", err)
	}

	// Parse response
	var result struct {
		StackResourceSummaries []CloudFormationStackResource `json:"StackResourceSummaries"`
	}

	// Try different response formats
	var output string
	if dataStr, ok := resp["data"].(string); ok && dataStr != "" {
		output = dataStr
	} else if outputStr, ok := resp["output"].(string); ok && outputStr != "" {
		output = outputStr
	} else if resultStr, ok := resp["result"].(string); ok && resultStr != "" {
		output = resultStr
	} else {
		return nil, fmt.Errorf("invalid response format from cloud CLI")
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse CloudFormation stack resources response: %w", err)
	}

	// Cache the result
	if cacheData, err := json.Marshal(result.StackResourceSummaries); err == nil {
		if err := common.CacheSet("cfn_stack_resources", cacheKey, cacheData); err != nil {
			s.logger.Warn("Failed to cache CloudFormation stack resources",
				"stack_name", stackName, "error", err)
		}
	}

	s.logger.Debug("Successfully fetched CloudFormation stack resources",
		"stack_name", stackName,
		"resource_count", len(result.StackResourceSummaries))

	return result.StackResourceSummaries, nil
}

// findCloudFormationManagedResource finds a node in the lookup for a CloudFormation stack resource
func (s *AWSSource) findCloudFormationManagedResource(resource CloudFormationStackResource, lookup *sources.NodeLookup) *core.DbNode {
	// Get mapping info for this resource type
	mapping, exists := cloudFormationResourceTypeMap[resource.ResourceType]
	if !exists {
		// Unknown resource type - try both lookup strategies
		if node, found := lookup.ByARN[resource.PhysicalResourceId]; found {
			return node
		}
		return lookup.ByResourceID[resource.PhysicalResourceId]
	}

	// Try ARN lookup first if PhysicalResourceId might be ARN
	if mapping.LookupByARN {
		if node, found := lookup.ByARN[resource.PhysicalResourceId]; found {
			return node
		}
	}

	// Try resource ID lookup
	if node, found := lookup.ByResourceID[resource.PhysicalResourceId]; found {
		return node
	}

	// For S3 buckets, the PhysicalResourceId is just the bucket name
	// Try looking up by name within the expected node type
	if mapping.NodeType == core.NodeTypeStorage && resource.ResourceType == "AWS::S3::Bucket" {
		for _, node := range lookup.ByNodeType[core.NodeTypeStorage] {
			if name, ok := node.Properties["name"].(string); ok && name == resource.PhysicalResourceId {
				return node
			}
		}
	}

	// For SQS queues, CloudFormation PhysicalResourceId is a queue URL
	// (e.g. https://sqs.us-east-1.amazonaws.com/123456789/queue-name), not an ARN.
	// Extract the queue name from the URL and look up by resource_id.
	if resource.ResourceType == "AWS::SQS::Queue" && strings.HasPrefix(resource.PhysicalResourceId, "https://") {
		parts := strings.Split(resource.PhysicalResourceId, "/")
		if queueName := parts[len(parts)-1]; queueName != "" {
			if node, found := lookup.ByResourceID[queueName]; found {
				return node
			}
		}
	}

	return nil
}

// stackResourcesMetaKey is written on the STACK's own cloud_resourses row by
// collector-server/cloud-collector/providers/aws/aws_cloudformation.go. The two
// live in separate Go modules, so the compiler cannot catch a rename -- the
// paired tests (cloudformation_from_db_test.go here, aws_cloudformation_test.go
// there) are the contract.
//
// On the stack's row and not on each managed resource's row, because
// etl_resources.go's upsert replaces meta wholesale per resource type: a second
// writer enriching an SQS queue's row would be clobbered by the next SQS
// discovery run.
const stackResourcesMetaKey = "StackResourceSummaries"

// stackResourcesFromDBEnabled is the kill switch, read in exactly one place --
// the first line of stackResourcesFromRow. A disabled flag makes that lookup a
// permanent miss, so no call site needs an `if flag` of its own and every caller
// falls through to its CLI path, the same way NewCloudTopologyStore returning nil
// does for the topology lookups (flow_sources/cloud_topology_store.go).
func stackResourcesFromDBEnabled() bool {
	return config.Config.FeatureKGCFNStackResourcesFromDB
}

// stackResourcesFromRow returns the stack's managed-resource list from the row
// the collector wrote, and whether the key was there at all.
//
// Presence is a different question from emptiness and the two must not be
// collapsed (the same rule as metaField in flow_sources/cloud_topology_store.go):
// an absent key means no collector ever captured the list -- an older collector,
// an on-prem deployment, or a ListStackResources call that failed -- so the
// caller falls back to the CLI. An empty array means the collector DID capture it
// and the stack genuinely manages nothing; falling back there would put a
// zero-resource stack on the CLI every build forever.
func (s *AWSSource) stackResourcesFromRow(node *core.DbNode) ([]CloudFormationStackResource, bool) {
	if !stackResourcesFromDBEnabled() {
		return nil, false
	}
	resourceID, _ := node.Properties["resource_id"].(string) // the StackId ARN
	if resourceID == "" {
		return nil, false
	}
	row, ok := s.metaByTypeAndID["stack"][resourceID]
	if !ok || len(row.Meta) == 0 {
		return nil, false
	}
	// Shallow probe, so Parameters/Outputs/Tags/DriftInformation are never decoded
	// and presence is answerable without parsing the value.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(row.Meta, &probe); err != nil {
		return nil, false
	}
	raw, present := probe[stackResourcesMetaKey]
	if !present {
		return nil, false
	}
	// The writer's contract is "omit the key or write an array", so a literal null
	// is evidence it misbehaved and is treated as absent rather than trusted.
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var summaries []CloudFormationStackResource
	if err := json.Unmarshal(raw, &summaries); err != nil {
		s.logger.Warn("stored CloudFormation stack resources are malformed, falling back to the CLI",
			"stack_id", resourceID, "error", err)
		return nil, false
	}
	if summaries == nil {
		summaries = []CloudFormationStackResource{}
	}
	return summaries, true
}

// createCloudFormationEdges creates edges from CloudFormation stacks to their managed resources
func (s *AWSSource) createCloudFormationEdges(reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) []*core.DbEdge {
	edges := make([]*core.DbEdge, 0)
	fromTable, fromCLI, cliFailed := 0, 0, 0

	for _, node := range nodes {
		// Get stack name from properties
		stackName, ok := node.Properties["name"].(string)
		if !ok || stackName == "" {
			s.logger.Debug("CloudFormation stack missing name, skipping",
				"node_id", node.ID)
			continue
		}

		// The collector captures this list once per discovery run and stores it on
		// the stack's own row. Reading it here is what removes one CLI process per
		// stack per build.
		stackResources, fromRow := s.stackResourcesFromRow(node)
		if fromRow {
			fromTable++
		} else {
			// CLI fallback, for an older collector, an on-prem deployment, or a
			// stack whose ListStackResources call failed.
			//
			// The region and stack-name guards live in this branch, not above it:
			// they exist only because those values are interpolated into a shell
			// command (#36573), and the table lookup builds no command. A stack with
			// no resolvable region, or a name the CLI validator rejects, still gets
			// its edges when the row can answer.
			region := nodeRegion(node)
			if region == "" {
				s.logger.Warn("CloudFormation stack has no region, skipping resource lookup",
					"stack_name", stackName,
					"node_id", node.ID)
				continue
			}

			var err error
			stackResources, err = s.fetchCloudFormationStackResources(reqCtx, req.CloudAccountID, region, stackName)
			if err != nil {
				cliFailed++
				if strings.Contains(err.Error(), "Stack with id") && strings.Contains(err.Error(), "does not exist") {
					// Logged at Warn, not Debug: now that the command carries the stack's
					// own region this genuinely means the stack is gone, and the count is
					// the signal that cloud resources need retiring from the graph.
					s.logger.Warn("CloudFormation stack not found, skipping resource processing",
						"stack_name", stackName,
						"region", region,
						"error", err)
					continue
				}

				s.logger.Warn("failed to fetch CloudFormation stack resources",
					"stack_name", stackName,
					"region", region,
					"error", err)
				continue
			}
			fromCLI++
		}

		s.logger.Debug("Processing CloudFormation stack resources",
			"stack_name", stackName,
			"resource_count", len(stackResources))

		// Create edges for each managed resource
		for _, resource := range stackResources {
			// Skip resources in failed or delete states
			if strings.HasPrefix(resource.ResourceStatus, "DELETE") ||
				strings.HasSuffix(resource.ResourceStatus, "FAILED") {
				continue
			}

			// Find the target node
			targetNode := s.findCloudFormationManagedResource(resource, lookup)
			if targetNode == nil {
				s.logger.Debug("CloudFormation managed resource not found in graph",
					"stack_name", stackName,
					"logical_id", resource.LogicalResourceId,
					"physical_id", resource.PhysicalResourceId,
					"resource_type", resource.ResourceType)
				continue
			}

			// Create MANAGES edge from stack to resource
			edges = append(edges, s.createEdge(node, targetNode, core.RelationshipManages,
				map[string]interface{}{
					"connection_type":     "cloudformation_managed",
					"logical_resource_id": resource.LogicalResourceId,
					"resource_type":       resource.ResourceType,
					"resource_status":     resource.ResourceStatus,
				}, req))

			s.logger.Debug("Created CloudFormation stack to resource edge",
				"stack_name", stackName,
				"target_name", targetNode.Properties["name"],
				"target_type", targetNode.NodeType,
				"logical_id", resource.LogicalResourceId)
		}
	}

	// Counts stacks served from the table against those that fell back to the CLI.
	// Without this a broken mapper is invisible: the fallback keeps the graph
	// correct while the CPU saving silently never arrives.
	s.logger.Info("CloudFormation stack resources resolved",
		"cloud_account_id", req.CloudAccountID,
		"stacks", len(nodes),
		"from_table", fromTable,
		"from_cli", fromCLI,
		"cli_failed", cliFailed,
		"edges", len(edges))

	return edges
}

// The inventory rows this file's resources claim: what each row's node
// type, concrete label and metadata extraction are (see kinds.go).
func init() {
	registerKind(resourceKind{
		Service:  "AWSCloudFormation",
		NodeType: core.NodeTypeInfraStack,
		Extract:  (*AWSSource).extractCloudFormationMetadata,
	})
	registerKind(resourceKind{
		Service:      "AWSCloudFormation",
		Types:        []string{"stack"},
		NodeType:     core.NodeTypeInfraStack,
		SpecificType: "CloudFormationStack",
		Extract:      (*AWSSource).extractCloudFormationMetadata,
	})
}

// The edges the account build asks this file's resources for (see dispatch.go).
func init() {
	// CloudFormation stacks manage the resources they created.
	registerEdgeStep(edgeStep{NodeType: core.NodeTypeInfraStack, Name: "cloudformation",
		Build: func(s *AWSSource, reqCtx *security.RequestContext, nodes []*core.DbNode, lookup *sources.NodeLookup, req *core.SourceBuildRequest) ([]*core.DbEdge, []*core.DbNode) {
			return s.createCloudFormationEdges(reqCtx, nodes, lookup, req), nil
		}})
}

// A stack MANAGES the resources it created, so those edges leave the stack but
// are only discoverable from the stack's own expansion.
func init() {
	registerEdgesBuiltElsewhere(core.EdgeKind{Relationship: core.RelationshipManages})
}
