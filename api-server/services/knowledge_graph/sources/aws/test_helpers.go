package aws

import (
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/security"
)

// TestHelper functions for integration testing — expose internal AWS converters
// for the e2e test package. Non-_test.go so the external test package can use them.

// AWSSourceTestHelper provides test helper methods for the AWS source.
type AWSSourceTestHelper struct {
	source *AWSSource
}

// NewAWSSourceTestHelper creates a new AWS source test helper.
func NewAWSSourceTestHelper(source *AWSSource) *AWSSourceTestHelper {
	return &AWSSourceTestHelper{source: source}
}

// ConvertResourcesToGraph exposes the internal convertResourcesToGraph method for testing.
func (h *AWSSourceTestHelper) ConvertResourcesToGraph(reqCtx *security.RequestContext, resources []sources.CloudResourceRow, req *core.SourceBuildRequest) ([]*core.DbNode, []*core.DbEdge) {
	return h.source.convertResourcesToGraph(reqCtx, resources, req)
}

// ConvertResourcesToGraphWithCache populates the AWS source's per-build meta
// cache (metaByType / metaByTypeAndID) from the rows — exactly as BuildGraph does
// before dispatching — then runs the converter. Edge builders that read the
// cache (IAM roles → RUNS_AS/ASSUMES, S3 notification → PUBLISHES_TO, ENI,
// elastic-ip, route-table, vpc-flow-log) are dead when the cache is empty, so
// the bare ConvertResourcesToGraph cannot exercise them; this helper can.
func (h *AWSSourceTestHelper) ConvertResourcesToGraphWithCache(reqCtx *security.RequestContext, resources []sources.CloudResourceRow, req *core.SourceBuildRequest) ([]*core.DbNode, []*core.DbEdge) {
	h.source.fillRowCache(resources)
	return h.source.convertResourcesToGraph(reqCtx, resources, req)
}
