package core

import (
	"fmt"

	"github.com/lib/pq"
)

// Delete candidates: graph nodes whose inventory row is gone.
//
// The change detector learns that an account lost resources when its active
// inventory count drops, but not which. These queries name them, so the
// detector can send one delete ref per resource instead of rebuilding the
// account. They only propose: the applier re-reads the inventory before it
// tombstones anything. Each returns at most limit ids; callers pass one more
// than they are willing to handle to detect overflow.
//
// nb_resource_id is free-form JSON, and a failed ::uuid cast would abort the
// caller's transaction, so ids are shape-checked in a materialized CTE before
// they are cast.

const uuidPattern = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`

// CloudDeleteCandidates returns the nb_resource_id (cloud_resourses.id) of the
// account's active nodes from the given sources whose inventory row is no longer
// active. A node whose row is missing altogether is left alone: nodes that
// account-wide passes synthesize have no row, and the account build owns them.
func (r *ResourcePatchRepository) CloudDeleteCandidates(tenantID, accountID string, nodeSources []string, limit int) ([]string, error) {
	return r.queryStrings(`
		WITH candidates AS MATERIALIZED (
			SELECT properties->>'nb_resource_id' AS row_id
			FROM knowledge_graph_node
			WHERE tenant_id = $1
			  AND cloud_account_id = $2
			  AND is_active = true
			  AND level = 'Tenant'
			  AND source = ANY($3::text[])
			  AND properties->>'nb_resource_id' ~ '`+uuidPattern+`'
		)
		SELECT DISTINCT c.row_id
		FROM candidates c
		JOIN cloud_resourses cr ON cr.id = c.row_id::uuid
		WHERE cr.account = $2 AND cr.is_active IS NOT TRUE
		LIMIT $4
	`, tenantID, accountID, pq.Array(nodeSources), limit)
}

// K8sWorkloadDeleteCandidates returns the workload id (k8s_workloads
// cloud_resource_id) of the account's active workload nodes of the given specific
// types that have no active workload row. The row may be inactive or already
// removed by the collector's stale-row cleanup.
func (r *ResourcePatchRepository) K8sWorkloadDeleteCandidates(tenantID, accountID string, specificTypes []string, limit int) ([]string, error) {
	return r.queryStrings(`
		WITH candidates AS MATERIALIZED (
			SELECT properties->>'nb_resource_id' AS workload_id
			FROM knowledge_graph_node
			WHERE tenant_id = $1
			  AND cloud_account_id = $2
			  AND is_active = true
			  AND level = 'Tenant'
			  AND source = 'k8s'
			  AND specific_type = ANY($3::text[])
			  AND properties->>'nb_resource_id' ~ '`+uuidPattern+`'
		)
		SELECT DISTINCT c.workload_id
		FROM candidates c
		WHERE NOT EXISTS (
			SELECT 1 FROM k8s_workloads w
			WHERE w.tenant_id = $1
			  AND w.cloud_account_id = $2
			  AND w.cloud_resource_id = c.workload_id::uuid
			  AND w.is_active = true
		)
		LIMIT $4
	`, tenantID, accountID, pq.Array(specificTypes), limit)
}

// K8sNodeDeleteCandidates returns the names of the account's active Kubernetes
// node graph nodes of the given specific type that have no active k8s_nodes row.
// Only nodes built from k8s_nodes are considered (they carry node_creation_time):
// the placeholder nodes the build synthesizes for a pod's spec.nodeName never had
// a row.
func (r *ResourcePatchRepository) K8sNodeDeleteCandidates(tenantID, accountID, specificType string, limit int) ([]string, error) {
	return r.queryStrings(`
		SELECT DISTINCT n.properties->>'name'
		FROM knowledge_graph_node n
		WHERE n.tenant_id = $1
		  AND n.cloud_account_id = $2
		  AND n.is_active = true
		  AND n.level = 'Tenant'
		  AND n.source = 'k8s'
		  AND n.specific_type = $3
		  AND n.properties ? 'node_creation_time'
		  AND COALESCE(n.properties->>'name', '') <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM k8s_nodes kn
			WHERE kn.tenant_id = $1
			  AND kn.cloud_account_id = $2
			  AND kn.name = n.properties->>'name'
			  AND kn.is_active = true
		  )
		LIMIT $4
	`, tenantID, accountID, specificType, limit)
}

func (r *ResourcePatchRepository) queryStrings(query string, args ...interface{}) ([]string, error) {
	if r.dbManager == nil {
		return nil, fmt.Errorf("database manager not initialized")
	}
	var out []string
	if err := r.dbManager.Db.Select(&out, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query delete candidates: %w", err)
	}
	return out, nil
}
