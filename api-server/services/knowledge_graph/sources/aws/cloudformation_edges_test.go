package aws

import (
	"encoding/json"
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackNodeFor builds an InfraStack node the way the classify path would, plus the
// row the collector would have written for it.
func stackNodeFor(t *testing.T, metaJSON, region string) (*AWSSource, *core.DbNode, *sources.NodeLookup) {
	t.Helper()
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	require.NoError(t, err)
	src.metaByTypeAndID = map[string]map[string]sources.CloudResourceRow{
		"stack": {testStackID: {ResourceID: testStackID, Type: "stack", Region: region, Meta: json.RawMessage(metaJSON)}},
	}

	stack := &core.DbNode{
		NodeType: core.NodeTypeInfraStack,
		Properties: map[string]interface{}{
			"name": "app", "resource_id": testStackID, "region": region,
		},
	}
	bucket := &core.DbNode{
		NodeType: core.NodeTypeStorage,
		Properties: map[string]interface{}{
			"name": "my-app-bucket", "resource_id": "my-app-bucket",
		},
	}
	lookup := sources.NewNodeLookup([]*core.DbNode{stack, bucket})
	return src, stack, lookup
}

// TestCreateCloudFormationEdges_FromTable is the core swap: the stored list
// produces the edge with no CLI call at all. The cloud collector URL is cleared,
// so any attempt to shell out fails immediately -- an edge appearing anyway proves
// it came from the row.
func TestCreateCloudFormationEdges_FromTable(t *testing.T) {
	defer disableCloudCLIForTest()()
	enableStackResourcesFromDB(t, true)
	src, stack, lookup := stackNodeFor(t, stackMetaWithResourcesJSON, "us-east-1")
	req := &core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"}

	edges := src.createCloudFormationEdges(nil, []*core.DbNode{stack}, lookup, req)

	require.Len(t, edges, 1, "the S3 bucket edge must come from the stored list")
	assert.Equal(t, core.RelationshipManages, edges[0].RelationshipType)
	assert.Equal(t, "cloudformation_managed", edges[0].Properties["connection_type"])
	assert.Equal(t, "AppBucket", edges[0].Properties["logical_resource_id"])
	assert.Equal(t, "AWS::S3::Bucket", edges[0].Properties["resource_type"])
	assert.Equal(t, "CREATE_COMPLETE", edges[0].Properties["resource_status"])
}

// TestCreateCloudFormationEdges_TableSkipsDeletedAndFailed proves the status
// filter still runs on the table path: the fixture's DELETE_COMPLETE SQS queue
// must not produce an edge, exactly as on the CLI path.
func TestCreateCloudFormationEdges_TableSkipsDeletedAndFailed(t *testing.T) {
	defer disableCloudCLIForTest()()
	enableStackResourcesFromDB(t, true)
	// The lookup this builds is discarded: this test needs the SQS queue in it, so
	// that a missing edge proves the status filter ran rather than proving the
	// target node was simply absent.
	src, stack, _ := stackNodeFor(t, stackMetaWithResourcesJSON, "us-east-1")

	queue := &core.DbNode{
		NodeType:   core.NodeTypeMessageQueue,
		Properties: map[string]interface{}{"name": "old", "resource_id": "old"},
	}
	lookup := sources.NewNodeLookup([]*core.DbNode{stack, queue})

	edges := src.createCloudFormationEdges(nil, []*core.DbNode{stack}, lookup,
		&core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"})

	for _, edge := range edges {
		assert.NotEqual(t, "OldQueue", edge.Properties["logical_resource_id"],
			"a DELETE_COMPLETE resource must not get an edge on either path")
	}
}

// TestCreateCloudFormationEdges_RegionGuardOnlyGatesTheCLI covers the one
// deliberate behaviour change. The region guard exists because the region is
// interpolated into a shell command (#36573); the table lookup builds no command,
// so a stack with no resolvable region still gets its edges from the row -- and
// still skips when the row cannot answer.
func TestCreateCloudFormationEdges_RegionGuardOnlyGatesTheCLI(t *testing.T) {
	req := &core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"}

	t.Run("row answers despite no region", func(t *testing.T) {
		defer disableCloudCLIForTest()()
		enableStackResourcesFromDB(t, true)
		src, stack, lookup := stackNodeFor(t, stackMetaWithResourcesJSON, "")
		delete(stack.Properties, "region")
		require.Empty(t, nodeRegion(stack), "fixture must have no resolvable region")

		edges := src.createCloudFormationEdges(nil, []*core.DbNode{stack}, lookup, req)

		assert.Len(t, edges, 1, "the table path needs no region")
	})

	t.Run("still skipped when the row cannot answer", func(t *testing.T) {
		defer disableCloudCLIForTest()()
		enableStackResourcesFromDB(t, true)
		src, stack, lookup := stackNodeFor(t, stackMetaNoKeyJSON, "")
		delete(stack.Properties, "region")

		edges := src.createCloudFormationEdges(nil, []*core.DbNode{stack}, lookup, req)

		assert.Empty(t, edges, "a region-less CLI lookup must still be refused")
	})
}

// TestCreateCloudFormationEdges_StackNameInvalidForCLI: validateStackName rejects
// underscores, so this stack could never be looked up over the CLI. The row can
// still answer for it.
func TestCreateCloudFormationEdges_StackNameInvalidForCLI(t *testing.T) {
	defer disableCloudCLIForTest()()
	enableStackResourcesFromDB(t, true)
	src, stack, lookup := stackNodeFor(t, stackMetaWithResourcesJSON, "us-east-1")
	stack.Properties["name"] = "my_stack"
	require.Error(t, validateStackName("my_stack"), "fixture must be a name the CLI path rejects")

	edges := src.createCloudFormationEdges(nil, []*core.DbNode{stack}, lookup,
		&core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"})

	assert.Len(t, edges, 1)
}

// TestCreateCloudFormationEdges_FallsBackWhenKeyAbsent is the zero-regression
// half: with no stored list the build takes the original CLI path. The collector
// URL is cleared so that call fails, which is the pre-existing behaviour for an
// unreachable collector -- no edges, no panic.
func TestCreateCloudFormationEdges_FallsBackWhenKeyAbsent(t *testing.T) {
	defer disableCloudCLIForTest()()
	enableStackResourcesFromDB(t, true)
	src, stack, lookup := stackNodeFor(t, stackMetaNoKeyJSON, "us-east-1")

	edges := src.createCloudFormationEdges(nil, []*core.DbNode{stack}, lookup,
		&core.SourceBuildRequest{TenantID: "t1", CloudAccountID: "a1"})

	assert.Empty(t, edges)
}
