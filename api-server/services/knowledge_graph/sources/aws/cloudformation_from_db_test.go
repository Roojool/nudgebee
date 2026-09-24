package aws

import (
	"encoding/json"
	"testing"

	"nudgebee/services/config"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The meta payloads below are copied from what the collector writes in
// collector-server/cloud-collector/providers/aws/aws_cloudformation.go. If the
// reader here drifts from that writer the graph silently loses MANAGES edges --
// the row is present, so nothing falls back to the CLI -- which is what these
// tests exist to catch. Stack and resource names are synthetic.
const (
	stackMetaWithResourcesJSON = `{
	  "StackId": "arn:aws:cloudformation:us-east-1:123456789012:stack/app/abc",
	  "StackName": "app",
	  "StackStatus": "CREATE_COMPLETE",
	  "CreationTime": "2026-01-02T03:04:05.123456789Z",
	  "StackResourcesCapturedAt": "2026-01-02T03:04:05.123456789Z",
	  "StackResourceSummaries": [
	    {"LogicalResourceId":"AppBucket","PhysicalResourceId":"my-app-bucket",
	     "ResourceType":"AWS::S3::Bucket","ResourceStatus":"CREATE_COMPLETE"},
	    {"LogicalResourceId":"OldQueue","PhysicalResourceId":"https://sqs.us-east-1.amazonaws.com/1/old",
	     "ResourceType":"AWS::SQS::Queue","ResourceStatus":"DELETE_COMPLETE"}
	  ]
	}`
	stackMetaEmptyResourcesJSON = `{"StackName":"empty","StackResourceSummaries":[]}`
	stackMetaNoKeyJSON          = `{"StackName":"legacy","StackStatus":"CREATE_COMPLETE"}`
	stackMetaNullJSON           = `{"StackName":"odd","StackResourceSummaries":null}`
	stackMetaMalformedJSON      = `{"StackName":"bad","StackResourceSummaries":{"not":"an array"}}`
)

const testStackID = "arn:aws:cloudformation:us-east-1:123456789012:stack/app/abc"

// enableStackResourcesFromDB turns the reader on for a test and restores whatever
// the process had. config.Config is a zero value under `go test`, so without this
// every lookup would miss for the wrong reason and these tests would pass
// vacuously.
func enableStackResourcesFromDB(t *testing.T, on bool) {
	t.Helper()
	prev := config.Config.FeatureKGCFNStackResourcesFromDB
	config.Config.FeatureKGCFNStackResourcesFromDB = on
	t.Cleanup(func() { config.Config.FeatureKGCFNStackResourcesFromDB = prev })
}

func stackSourceWithMeta(t *testing.T, metaJSON string) (*AWSSource, *core.DbNode) {
	t.Helper()
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	require.NoError(t, err)
	src.metaByTypeAndID = map[string]map[string]sources.CloudResourceRow{
		"stack": {testStackID: {ResourceID: testStackID, Type: "stack", Region: "us-east-1", Meta: json.RawMessage(metaJSON)}},
	}
	node := &core.DbNode{
		NodeType:   core.NodeTypeInfraStack,
		Properties: map[string]interface{}{"name": "app", "resource_id": testStackID},
	}
	return src, node
}

func TestStackResourcesFromRow_Hit(t *testing.T) {
	enableStackResourcesFromDB(t, true)
	src, node := stackSourceWithMeta(t, stackMetaWithResourcesJSON)

	got, ok := src.stackResourcesFromRow(node)

	require.True(t, ok)
	require.Len(t, got, 2)
	assert.Equal(t, "AppBucket", got[0].LogicalResourceId)
	assert.Equal(t, "my-app-bucket", got[0].PhysicalResourceId)
	assert.Equal(t, "AWS::S3::Bucket", got[0].ResourceType)
	assert.Equal(t, "CREATE_COMPLETE", got[0].ResourceStatus)
	// The reader does not filter. createCloudFormationEdges applies the
	// DELETE*/*FAILED skip, exactly as it does on the CLI path -- same filter,
	// same input set, which is what makes the two paths interchangeable.
	assert.Equal(t, "DELETE_COMPLETE", got[1].ResourceStatus)
}

// TestStackResourcesFromRow_EmptyIsAHit is the single most important assertion in
// this change. Returning false here would put every zero-resource stack back on
// the CLI, on every graph build, forever.
func TestStackResourcesFromRow_EmptyIsAHit(t *testing.T) {
	enableStackResourcesFromDB(t, true)
	src, node := stackSourceWithMeta(t, stackMetaEmptyResourcesJSON)

	got, ok := src.stackResourcesFromRow(node)

	require.True(t, ok, "an empty list is a real answer, not a miss")
	assert.Empty(t, got)
}

func TestStackResourcesFromRow_MissesFallBackToCLI(t *testing.T) {
	tests := []struct {
		name string
		meta string
	}{
		{"absent key -- older collector or a failed capture", stackMetaNoKeyJSON},
		{"literal null -- the writer's contract is omit-or-array", stackMetaNullJSON},
		{"malformed value", stackMetaMalformedJSON},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enableStackResourcesFromDB(t, true)
			src, node := stackSourceWithMeta(t, tc.meta)

			got, ok := src.stackResourcesFromRow(node)

			assert.False(t, ok)
			assert.Nil(t, got)
		})
	}
}

func TestStackResourcesFromRow_NoRowForStack(t *testing.T) {
	enableStackResourcesFromDB(t, true)
	src, node := stackSourceWithMeta(t, stackMetaWithResourcesJSON)
	node.Properties["resource_id"] = "arn:aws:cloudformation:us-east-1:1:stack/other/zzz"

	_, ok := src.stackResourcesFromRow(node)

	assert.False(t, ok, "a stack with no loaded row must fall through to the CLI")
}

func TestStackResourcesFromRow_FlagOff(t *testing.T) {
	enableStackResourcesFromDB(t, false)
	src, node := stackSourceWithMeta(t, stackMetaWithResourcesJSON)

	_, ok := src.stackResourcesFromRow(node)

	assert.False(t, ok, "the kill switch must make every lookup a permanent miss")
}

// TestStackResourcesMetaKeyIsStable is the consumer half of the contract pinned by
// collector-server/cloud-collector/providers/aws/aws_cloudformation_test.go. If
// the two drift, one of those two tests fails.
func TestStackResourcesMetaKeyIsStable(t *testing.T) {
	assert.Equal(t, "StackResourceSummaries", stackResourcesMetaKey)
}
