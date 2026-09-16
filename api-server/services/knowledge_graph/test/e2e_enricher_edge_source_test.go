package test

// Contract: every edge a cross-source enricher emits is stamped with that
// enricher's GetName().
//
// The account-scoped edge sweep decides which stale edges it may tombstone by
// matching edge.source against the names of the producers that ran
// (core.collectRanEdgeSources, fed by enricher.GetName()). An enricher whose
// edges carry any other string is invisible to that sweep: its stale edges are
// never cleaned by an account rebuild. That is exactly how k8s_ec2
// ("k8s_ec2_enricher") and aws_lb_k8s ("aws_lb_k8s_enricher") went unswept, and
// the earlier unit test missed it because it fed hand-typed stamps instead of
// what GetName() really returns.
//
// So this runs the REAL enrichers on real fixtures and inspects what they
// actually emit. aws_lb_pod_target needs a live cloud CLI and Prometheus and
// cannot run here; it stamps e.GetName() structurally.

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
	"nudgebee/services/knowledge_graph/sources/k8s"
)

func TestEnrichersStampEdgesWithTheirOwnName(t *testing.T) {
	cases := []struct {
		name     string
		enricher core.CrossSourceEnricherInterface
		graph    func(t *testing.T) ([]*core.DbNode, []*core.DbEdge)
	}{
		{
			name:     "k8s_ec2",
			enricher: sources.NewK8sEC2Enricher(e2eDiscardLogger()),
			graph: func(t *testing.T) ([]*core.DbNode, []*core.DbEdge) {
				inputDir := filepath.Join("testdata", "e2e", "k8s_plus_aws", "input")
				awsNodes, awsEdges := buildAWSGraph(t,
					loadCloudResourceRows(t, filepath.Join(inputDir, "aws.json")),
					&core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eAWSAccount})
				k8sNodes, k8sEdges := buildK8sGraph(t, nil,
					loadK8sNodeRows(t, filepath.Join(inputDir, "k8s_nodes.json")),
					&core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eK8sAccount})
				return append(awsNodes, k8sNodes...), append(awsEdges, k8sEdges...)
			},
		},
		{
			name:     "aws_lb_k8s",
			enricher: sources.NewLoadBalancerK8sEnricher(e2eDiscardLogger()),
			graph: func(t *testing.T) ([]*core.DbNode, []*core.DbEdge) {
				awsNodes, awsEdges := buildAWSGraph(t,
					loadCloudResourceRows(t, filepath.Join("testdata", "e2e", "aws_lb_k8s", "input", "aws.json")),
					&core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eAWSAccount})
				// Mirrors TestE2E_TierA_AWSLBToK8s: a Service my-svc/default and the
				// my-eks cluster, which is what the enricher links the LB and EKS to.
				workloads := []k8s.K8sWorkloadRow{{
					ID: "my-svc", Kind: "Service", Namespace: "default", Name: "my-svc",
					ClusterName: "my-eks", IsActive: true, Meta: json.RawMessage("{}"), Labels: json.RawMessage("{}"),
				}}
				kNodes, kEdges := buildK8sGraph(t, workloads,
					loadK8sNodeRows(t, filepath.Join("testdata", "e2e", "aws_lb_k8s", "input", "k8s_nodes.json")),
					&core.SourceBuildRequest{TenantID: e2eTenantID, CloudAccountID: e2eK8sAccount})
				return append(awsNodes, kNodes...), append(awsEdges, kEdges...)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer disableCloudCLI()()

			if got := tc.enricher.GetName(); got != tc.name {
				t.Fatalf("GetName() = %q, want %q", got, tc.name)
			}

			nodes, edges := tc.graph(t)
			before := make(map[*core.DbEdge]struct{}, len(edges))
			for _, e := range edges {
				before[e] = struct{}{}
			}

			_, enriched, err := tc.enricher.EnrichCrossSources(createTestRequestContext(e2eTenantID), nodes, edges, e2eTenantID)
			if err != nil {
				t.Fatalf("EnrichCrossSources: %v", err)
			}

			emitted := 0
			for _, e := range enriched {
				if _, existed := before[e]; existed {
					continue
				}
				emitted++
				if e.Source != tc.enricher.GetName() {
					t.Errorf("%s emitted a %s edge stamped %q; it must be stamped %q or the account-scoped sweep cannot recognise it",
						tc.name, e.RelationshipType, e.Source, tc.enricher.GetName())
				}
			}
			if emitted == 0 {
				t.Fatalf("%s emitted no edges on this fixture; the contract is not being exercised", tc.name)
			}
		})
	}
}
