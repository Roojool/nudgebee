package core

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lib/pq"

	"nudgebee/services/internal/database"
)

// Persistence primitives for per-resource knowledge-graph updates.
//
// A per-resource update changes one node (and, for a few types, its outgoing
// edges) without rebuilding the account. The decision logic lives in the
// resourcepatch package, because it needs the per-cloud node builders and core
// cannot import sources; this file only holds the SQL.

// StoredNode is an active node as it is persisted, plus the sync version it was
// last stamped with.
type StoredNode struct {
	*DbNode
	LastSyncVersion int64
}

// EdgeKind identifies one family of edges a node emits: a relationship type and,
// optionally, the connection_type property that tells same-typed edges apart
// (an EC2 instance is HOSTED_ON its VPC, subnet and security groups). An empty
// ConnectionType matches every edge of the relationship type.
type EdgeKind struct {
	Relationship   RelationshipType
	ConnectionType string
}

// EdgeSignature is the set of outgoing edges one producer derives from a single
// node. A per-resource update re-emits exactly these edges and tombstones the
// ones it did not re-emit, so the signature must never include an edge the
// per-resource path cannot rebuild (for instance one that needs a live cloud API
// call): that edge would be tombstoned and not re-created.
type EdgeSignature struct {
	Source string
	Kinds  []EdgeKind
}

// ResourcePatchRepository owns the SQL behind per-resource updates.
type ResourcePatchRepository struct {
	dbManager *database.DatabaseManager
	logger    *slog.Logger
}

// NewResourcePatchRepository constructs a repository bound to a database manager.
func NewResourcePatchRepository(dbManager *database.DatabaseManager, logger *slog.Logger) *ResourcePatchRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &ResourcePatchRepository{dbManager: dbManager, logger: logger}
}

// maxProviderIDMatches bounds the lookup. More than one match already means the
// id is ambiguous and the caller falls back to an account rebuild, so there is no
// reason to read further.
const maxProviderIDMatches = 3

// FindActiveNodesByProviderID returns the account's active nodes whose provider
// id matches: properties.resource_id (the cloud provider's id), nb_resource_id
// (cloud_resourses.id, or k8s_workloads.cloud_resource_id) or, case-insensitively,
// the ARN. Served by the partial indexes from migration V945.
func (r *ResourcePatchRepository) FindActiveNodesByProviderID(tenantID, accountID, providerID string) ([]*StoredNode, error) {
	if tenantID == "" || accountID == "" || providerID == "" {
		return nil, fmt.Errorf("tenant id, account id and provider id are required")
	}
	return r.queryStoredNodes(`
		WHERE tenant_id = $1
		  AND cloud_account_id = $2
		  AND is_active = true
		  AND level = 'Tenant'
		  AND (properties->>'resource_id' = $3
		       OR properties->>'nb_resource_id' = $3
		       OR lower(properties->>'arn') = lower($3))
		LIMIT $4
	`, tenantID, accountID, providerID, maxProviderIDMatches)
}

// FindActiveNodeByID returns the active tenant-level node with the id, or nil.
func (r *ResourcePatchRepository) FindActiveNodeByID(tenantID, nodeID string) (*StoredNode, error) {
	nodes, err := r.queryStoredNodes(`
		WHERE tenant_id = $1 AND id = $2 AND is_active = true AND level = 'Tenant'
	`, tenantID, nodeID)
	if err != nil || len(nodes) == 0 {
		return nil, err
	}
	return nodes[0], nil
}

// FindActiveNodesByName returns the account's active tenant-level nodes of one
// source and specific type with the name. Used for resources whose nodes carry
// no provider id, such as Kubernetes nodes. Served by the query_attributes name
// index.
func (r *ResourcePatchRepository) FindActiveNodesByName(tenantID, accountID, source, specificType, name string) ([]*StoredNode, error) {
	if tenantID == "" || accountID == "" || name == "" {
		return nil, fmt.Errorf("tenant id, account id and name are required")
	}
	return r.queryStoredNodes(`
		WHERE tenant_id = $1
		  AND cloud_account_id = $2
		  AND is_active = true
		  AND level = 'Tenant'
		  AND source = $3
		  AND specific_type = $4
		  AND query_attributes->>'name' = $5
		LIMIT $6
	`, tenantID, accountID, source, specificType, name, maxProviderIDMatches)
}

// queryStoredNodes runs a knowledge_graph_node query with the stored-node
// projection. where is everything after FROM.
func (r *ResourcePatchRepository) queryStoredNodes(where string, args ...interface{}) ([]*StoredNode, error) {
	if r.dbManager == nil {
		return nil, fmt.Errorf("database manager not initialized")
	}
	rows, err := r.dbManager.Query(`
		SELECT id, properties, cloud_account_id, tenant_id, unique_key, node_type, level,
		       COALESCE(source, ''), COALESCE(specific_type, ''), COALESCE(last_sync_version, 0)::bigint
		FROM knowledge_graph_node
	`+where, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			r.logger.Warn("failed to close rows", "error", closeErr)
		}
	}()

	nodes := make([]*StoredNode, 0, 1)
	for rows.Next() {
		node := &DbNode{}
		var propertiesJSON []byte
		var version int64
		if err := rows.Scan(
			&node.ID,
			&propertiesJSON,
			&node.CloudAccountID,
			&node.TenantID,
			&node.UniqueKey,
			&node.NodeType,
			&node.Level,
			&node.Source,
			&node.SpecificType,
			&version,
		); err != nil {
			return nil, fmt.Errorf("failed to scan node: %w", err)
		}
		if err := json.Unmarshal(propertiesJSON, &node.Properties); err != nil {
			return nil, fmt.Errorf("failed to unmarshal node properties: %w", err)
		}
		nodes = append(nodes, &StoredNode{DbNode: node, LastSyncVersion: version})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating node rows: %w", err)
	}
	return nodes, nil
}

// TombstoneNodeWithIncidentEdges marks a node inactive together with every active
// edge that starts or ends at it, in one transaction.
//
// This deliberately includes behavioural edges (CALLS, PUBLISHES_TO, ...) that
// the build sweeps leave to the time-based expiry: the node is gone, and an
// active edge pointing at a tombstoned node is exactly what traversal has to
// guard against. Returns the number of edges tombstoned.
func (r *ResourcePatchRepository) TombstoneNodeWithIncidentEdges(tenantID, nodeID string) (int64, error) {
	if r.dbManager == nil {
		return 0, fmt.Errorf("database manager not initialized")
	}
	tx, err := r.dbManager.BeginTx()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			database.LogRollback(tx, r.logger)
		}
	}()

	if _, err := tx.Exec(`
		UPDATE knowledge_graph_node
		SET is_active = false
		WHERE tenant_id = $1 AND id = $2 AND is_active = true
	`, tenantID, nodeID); err != nil {
		return 0, fmt.Errorf("failed to tombstone node: %w", err)
	}

	result, err := tx.Exec(`
		UPDATE knowledge_graph_edge
		SET is_active = false
		WHERE tenant_id = $1
		  AND is_active = true
		  AND (source_node_id = $2 OR destination_node_id = $2)
	`, tenantID, nodeID)
	if err != nil {
		return 0, fmt.Errorf("failed to tombstone incident edges: %w", err)
	}
	edges, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf(errRowsAffected, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return edges, nil
}

// TombstoneOutgoingEdges marks inactive the node's active outgoing edges that
// match the signature and were stamped below syncVersion. Called after the
// re-emitted edges were saved at syncVersion, so what remains below it is exactly
// the set the resource no longer has — and there is no moment in which a still
// valid edge reads as inactive.
func (r *ResourcePatchRepository) TombstoneOutgoingEdges(tenantID, nodeID string, sig EdgeSignature, syncVersion int64) (int64, error) {
	if r.dbManager == nil {
		return 0, fmt.Errorf("database manager not initialized")
	}
	if sig.Source == "" || len(sig.Kinds) == 0 {
		return 0, fmt.Errorf("edge signature needs a source and at least one kind")
	}

	rels := make([]string, len(sig.Kinds))
	connectionTypes := make([]string, len(sig.Kinds))
	for i, k := range sig.Kinds {
		rels[i] = string(k.Relationship)
		connectionTypes[i] = k.ConnectionType
	}

	result, err := r.dbManager.Exec(`
		UPDATE knowledge_graph_edge e
		SET is_active = false
		WHERE e.tenant_id = $1
		  AND e.source_node_id = $2
		  AND e.is_active = true
		  AND e.level = 'Tenant'
		  AND e.source = $3
		  AND e.last_sync_version < $4
		  AND EXISTS (
		        SELECT 1 FROM unnest($5::text[], $6::text[]) AS k(rel, connection_type)
		        WHERE k.rel = e.relationship_type
		          AND (k.connection_type = '' OR k.connection_type = e.properties->>'connection_type')
		  )
	`, tenantID, nodeID, sig.Source, syncVersion, pq.Array(rels), pq.Array(connectionTypes))
	if err != nil {
		return 0, fmt.Errorf("failed to tombstone outgoing edges: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf(errRowsAffected, err)
	}
	return affected, nil
}

// LoadActiveNodesByTypes returns the account's active tenant-level nodes of the
// given types, with the same lean projection partial builds use for context
// nodes. These are the edge targets a per-resource update resolves against.
func (r *ResourcePatchRepository) LoadActiveNodesByTypes(tenantID, accountID string, types []NodeType) ([]*DbNode, error) {
	if r.dbManager == nil {
		return nil, fmt.Errorf("database manager not initialized")
	}
	if len(types) == 0 {
		return nil, nil
	}
	typeNames := make([]string, len(types))
	for i, t := range types {
		typeNames[i] = string(t)
	}

	rows, err := r.dbManager.Query(`
		SELECT id, properties, cloud_account_id, tenant_id, unique_key, node_type, level,
		       COALESCE(source, ''), COALESCE(specific_type, '')
		FROM knowledge_graph_node
		WHERE tenant_id = $1
		  AND cloud_account_id = $2
		  AND level = 'Tenant'
		  AND is_active = true
		  AND node_type = ANY($3::text[])
	`, tenantID, accountID, pq.Array(typeNames))
	if err != nil {
		return nil, fmt.Errorf("failed to query nodes by type: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			r.logger.Warn("failed to close rows", "error", closeErr)
		}
	}()

	nodes := make([]*DbNode, 0)
	for rows.Next() {
		node := &DbNode{}
		var propertiesJSON []byte
		if err := rows.Scan(
			&node.ID,
			&propertiesJSON,
			&node.CloudAccountID,
			&node.TenantID,
			&node.UniqueKey,
			&node.NodeType,
			&node.Level,
			&node.Source,
			&node.SpecificType,
		); err != nil {
			return nil, fmt.Errorf("failed to scan node: %w", err)
		}
		if err := json.Unmarshal(propertiesJSON, &node.Properties); err != nil {
			return nil, fmt.Errorf("failed to unmarshal node properties: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating node rows: %w", err)
	}
	return nodes, nil
}

// CurrentSyncVersion returns the highest last_sync_version across the tenant's
// enabled filters — the version the most recent completed build stamped. A
// build in progress stamps CurrentSyncVersion+1.
func (r *ResourcePatchRepository) CurrentSyncVersion(tenantID string) (int64, error) {
	if r.dbManager == nil {
		return 0, fmt.Errorf("database manager not initialized")
	}
	var version int64
	if err := r.dbManager.Db.Get(&version, `
		SELECT COALESCE(MAX(last_sync_version), 0)::bigint
		FROM knowledge_graph_tenant_filters
		WHERE tenant_id = $1 AND enabled = true
	`, tenantID); err != nil {
		return 0, fmt.Errorf("failed to read current sync version: %w", err)
	}
	return version, nil
}
