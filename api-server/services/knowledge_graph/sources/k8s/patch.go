package k8s

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"nudgebee/services/internal/database"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// Kubernetes per-resource update policy. The rules it applies live with the
// code they describe: workload kinds and the workload edge signature in
// workload.go, node lookup in node.go. This file only wires them to
// sources.K8sPatchPolicy.

func init() {
	sources.RegisterK8sPatchPolicy(func(logger *slog.Logger) (sources.K8sPatchPolicy, error) {
		// Configured exactly as the source registry configures it for a graph build.
		source, err := NewK8sSource(K8sSourceConfig{}, logger)
		if err != nil {
			return nil, err
		}
		return &patchPolicy{source: source}, nil
	})
}

// patchPolicy implements sources.K8sPatchPolicy.
type patchPolicy struct {
	source *K8sSource
}

func (p *patchPolicy) Source() string { return p.source.GetName() }

func (p *patchPolicy) DetectedWorkloadKinds() []string {
	return detectedWorkloadKinds(p.source.config.WorkloadKinds)
}

func (p *patchPolicy) WorkloadSpecificTypes(kinds []string) []string {
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, workloadSpecificType(kind))
	}
	return out
}

func (p *patchPolicy) NodeSpecificType() string {
	return k8sNodeSchema.SpecificType
}

func (p *patchPolicy) HasActiveNode(tenantID, accountID, name string) (bool, error) {
	return hasActiveK8sNode(tenantID, accountID, name)
}

func (p *patchPolicy) BuildWorkload(tenantID, accountID, workloadID string) (*sources.WorkloadPatch, error) {
	row, err := loadWorkloadRow(tenantID, accountID, workloadID)
	if err != nil {
		return nil, err
	}
	if !workloadKindFor(row.Kind).Patchable {
		return &sources.WorkloadPatch{Kind: row.Kind}, nil
	}
	frameworks, err := GetWorkloadFrameworks(accountID)
	if err != nil {
		// The account build proceeds without them, but the datastore facet it
		// writes would then differ from the stored one; let the caller rebuild.
		return nil, fmt.Errorf("failed to load workload frameworks: %w", err)
	}
	req := &core.SourceBuildRequest{TenantID: tenantID, CloudAccountID: accountID}
	patch := p.source.buildWorkloadPatch(row, frameworks, req)
	return patch, nil
}

// loadWorkloadRow reads one active workload row with the account build's
// projection.
func loadWorkloadRow(tenantID, accountID, workloadID string) (*K8sWorkloadRow, error) {
	dbManager, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, fmt.Errorf("failed to get database manager: %w", err)
	}
	var row K8sWorkloadRow
	err = dbManager.Db.Get(&row, workloadRowSelect+`
		WHERE w.tenant_id = $1
		  AND w.cloud_account_id = $2
		  AND w.cloud_resource_id = $3
		  AND w.is_active = true
	`, tenantID, accountID, workloadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sources.ErrNotInInventory
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load k8s workload: %w", err)
	}
	return &row, nil
}

// buildWorkloadPatch builds the node and own edges for one workload row with the
// functions convertWorkloadsToGraph and BuildGraph use.
func (s *K8sSource) buildWorkloadPatch(row *K8sWorkloadRow, frameworks map[string]string, req *core.SourceBuildRequest) *sources.WorkloadPatch {
	node := s.createNodeFromWorkload(row, frameworks, req)
	namespace := s.createNamespaceNode(row.Namespace, row.ClusterName, req)

	nodes := make([]*core.DbNode, 0)
	edges := make([]*core.DbEdge, 0)
	repositoryNodes := make(map[string]*core.DbNode)
	helmChartNodes := make(map[string]*core.DbNode)
	s.appendWorkloadOwnEdges(row, node, namespace, &repositoryNodes, &helmChartNodes, &nodes, &edges, req)

	imageNodes, imageEdges := s.createContainerImageNodesAndEdges([]*core.DbNode{node}, req)
	nodes = append(nodes, imageNodes...)
	edges = append(edges, imageEdges...)

	return &sources.WorkloadPatch{
		Kind:      row.Kind,
		Patchable: true,
		Node:      node,
		Namespace: namespace,
		NewNodes:  nodes,
		Edges:     edges,
		Signature: workloadEdgeSignature,
	}
}
