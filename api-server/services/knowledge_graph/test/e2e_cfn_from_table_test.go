package test

import (
	"path/filepath"
	"strings"
	"testing"

	"nudgebee/services/config"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources/aws"
)

// TestE2E_MockedInfra_AWSManagesFromTable is the sibling of
// TestE2E_MockedInfra_AWSManages, and together they are the zero-regression proof
// for moving the stack -> managed-resource mapping into the collector.
//
// Same four rows, same golden graph. The only differences are that each stack's
// meta carries the StackResourceSummaries the collector now writes, and that the
// fake collector here REFUSES every command. A build that still produces the
// golden edges with the CLI wired to explode is the strongest available statement
// that the table path is self-sufficient — nothing is quietly falling back.
//
// Its sibling keeps the opposite guarantee: those fixtures carry no
// StackResourceSummaries, so they still exercise the CLI path unchanged.
func TestE2E_MockedInfra_AWSManagesFromTable(t *testing.T) {
	srv := fakeCollector(t, func(command string) (string, bool) {
		// Scoped to CloudFormation on purpose: the same build legitimately issues
		// unrelated commands from other edge builders (IAM instance profiles, for
		// one), and failing on those would test something else entirely.
		if strings.Contains(command, "cloudformation list-stack-resources") {
			t.Errorf("the table path must not shell out, but it ran: %q", command)
		}
		return "", false
	})
	defer srv.Close()

	prevURL := config.Config.CloudCollectorServerUrl
	config.Config.CloudCollectorServerUrl = srv.URL
	defer func() { config.Config.CloudCollectorServerUrl = prevURL }()

	prevFlag := config.Config.FeatureKGCFNStackResourcesFromDB
	config.Config.FeatureKGCFNStackResourcesFromDB = true
	defer func() { config.Config.FeatureKGCFNStackResourcesFromDB = prevFlag }()

	req := &core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eAWSAccount}
	resources := loadCloudResourceRows(t, filepath.Join("testdata", "e2e", "aws_manages_from_table", "input", "aws.json"))

	awsSource, err := aws.NewAWSSource(aws.AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}
	h := aws.NewAWSSourceTestHelper(awsSource)
	nodes, edges := h.ConvertResourcesToGraphWithCache(mockedInfraReqCtx(), resources, req)
	nodes, edges = mergeGraph(nodes, edges, mergeOpts{})

	assertGoldenGraph(t, e2eGoldenPath("aws_manages_from_table"), nodes, edges)

	// The same two MANAGES edges the CLI path produces, one per region — built
	// here without a single cloud call.
	manages := 0
	for _, e := range edges {
		if e.RelationshipType == core.RelationshipManages {
			manages++
		}
	}
	if manages != 2 {
		t.Errorf("aws_manages_from_table: expected 2 CloudFormation InfraStack→Storage MANAGES edges, got %d", manages)
	}
}
