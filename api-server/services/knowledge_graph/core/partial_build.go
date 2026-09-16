package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/lib/pq"
)

// Partial (account-scoped) build support.
//
// A partial build rebuilds a subset of a tenant's cloud accounts instead of the
// whole tenant. Three things make that safe:
//
//  1. Context: every OTHER account's active nodes are loaded from the database
//     and appended to the in-memory graph, so the cross-source enrichers and the
//     cross-account relationship rules — which only ever look at the in-memory
//     node slice — still find their counterparties. Context nodes are read-only:
//     they are handed to SaveEdges (which drops edges whose endpoints it cannot
//     see) but withheld from SaveNodes.
//  2. Scoped node sweep: markInactiveNodes already filters by cloud_account_id;
//     the partial path additionally suppresses its tenant-scoped-source
//     exception so a build that never ran the GitHub/identity/ownership
//     producers cannot tombstone their rows.
//  3. Scoped edge sweep: markInactiveInfraEdgesScoped only considers edges
//     stamped by a producer that actually ran this build AND attached to the
//     rebuilt accounts.
//
// The hourly tenant-wide build remains the reconciliation pass: anything a
// scoped sweep conservatively skips is corrected there.

// tenantScopedEnricherNames are cross-source enrichers that derive their nodes
// and edges from tenant-wide tables (users, user_groups, integrations) rather
// than from any one cloud account. A partial build skips them — rebuilding them
// from an account-scoped graph would produce a partial picture — and therefore
// must also keep their edges out of the sweep's ran-set.
var tenantScopedEnricherNames = map[string]bool{
	"identity":  true,
	"ownership": true,
}

// loadContextNodes fetches every active node for the tenant that does NOT belong
// to one of excludeAccountIDs. These are the "other accounts" a partial build
// must be able to see in memory to resolve cross-account relationships.
//
// The projection is deliberately lean: no labels, query_attributes or
// ontology_attributes, because no enricher, cross-account rule or collapse pass
// reads them — they are write-side concerns. Unlike GetNodesByTenant this does
// NOT exclude inferred nodes: an inferred VPC or subnet is a legitimate edge
// endpoint, and dropping it here would make SaveEdges discard edges pointing at
// it as orphans.
//
// Returns the nodes plus the set of their IDs, which is what splitOwnNodes uses
// later to decide what must not be persisted.
func (s *Service) loadContextNodes(tenantID string, excludeAccountIDs []string) ([]*DbNode, map[string]struct{}, error) {
	if s.dbManager == nil {
		return nil, nil, fmt.Errorf("database manager not initialized")
	}

	query := `
		SELECT id, properties, cloud_account_id, tenant_id, unique_key, node_type, level,
		       COALESCE(source, ''), COALESCE(specific_type, '')
		FROM knowledge_graph_node
		WHERE tenant_id = $1
		  AND level = 'Tenant'
		  AND is_active = true
		  AND cloud_account_id <> ALL($2::uuid[])
	`

	rows, err := s.dbManager.Query(query, tenantID, pq.Array(excludeAccountIDs))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query context nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("Failed to close rows", "error", closeErr)
		}
	}()

	nodes := make([]*DbNode, 0)
	contextIDs := make(map[string]struct{})

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
			return nil, nil, fmt.Errorf("failed to scan context node: %w", err)
		}

		if err := json.Unmarshal(propertiesJSON, &node.Properties); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal context node properties: %w", err)
		}

		nodes = append(nodes, node)
		contextIDs[node.ID] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("error iterating context node rows: %w", err)
	}

	return nodes, contextIDs, nil
}

// splitOwnNodes returns the nodes a partial build is allowed to persist: every
// node that is not a context node.
//
// Identity is by node ID rather than a flag on DbNode, because a flag does not
// survive the build: DeduplicateNodesWithIDMapping merges by UniqueKey and the
// surviving struct may be either copy, and cross-source enrichers build fresh
// DbNode literals rather than copying an existing one. An ID set captured at load
// time is unaffected by both.
//
// How a context node and a freshly built node can meet: a UniqueKey embeds the
// node's own cloud account, and a node ID is derived from (UniqueKey, tenant,
// account), so a rebuilt account's node can never share an identity with a
// context node from a different account. What does happen is that a cross-source
// enricher (aws_lb_pod_target) synthesizes a node for an account this build did
// NOT rebuild — for instance the K8s Service behind an AWS load balancer. That
// synthesized node has exactly the same ID as the context node already loaded for
// it, so dedup folds the two together, the ID stays in contextIDs, and it is
// correctly withheld here while edges pointing at it are repointed onto the real
// row. Persisting it instead would stamp another account's row with this build's
// sync version and hide its staleness from that account's own sweep.
func splitOwnNodes(nodes []*DbNode, contextIDs map[string]struct{}) []*DbNode {
	if len(contextIDs) == 0 {
		return nodes
	}
	own := make([]*DbNode, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if _, isContext := contextIDs[n.ID]; isContext {
			continue
		}
		own = append(own, n)
	}
	return own
}

// dropContextOnlyEdges removes edges whose BOTH endpoints are context nodes.
// Such an edge belongs entirely to accounts this build did not rebuild;
// re-upserting it would stamp it with the new sync version for no reason and
// hide genuine staleness from the next tenant-wide sweep.
func dropContextOnlyEdges(edges []*DbEdge, contextIDs map[string]struct{}) []*DbEdge {
	if len(contextIDs) == 0 {
		return edges
	}
	kept := make([]*DbEdge, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			continue
		}
		_, srcIsContext := contextIDs[e.SourceNodeID]
		_, dstIsContext := contextIDs[e.DestinationNodeID]
		if srcIsContext && dstIsContext {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// collectRanEdgeSources returns the set of edge `source` stamps whose producer
// actually ran during this build. It is the ran-set the scoped edge sweep keys
// on: an edge stamped by a producer that did not run is protected rather than
// tombstoned, because this build has no evidence about whether it is still
// valid.
//
// For a partial build this deliberately never contains the tenant-scoped
// producers (identity, ownership, github, gitlab, pagerduty), `manual`, or any
// flow source — none of them run in a partial build.
func collectRanEdgeSources(response *BuildResponse, ranEnrichers []string) []string {
	seen := make(map[string]struct{})
	add := func(name string) {
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}

	if response != nil {
		for _, meta := range response.AccountMetadata {
			for _, src := range meta.SourcesBuilt {
				add(src)
			}
		}
	}
	for _, name := range ranEnrichers {
		add(name)
	}
	// Cross-account relationship rules run on every build that reaches Phase 3.
	add(CrossAccountEdgeSource)

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// markInactiveInfraEdgesScoped is the partial-build counterpart of
// markInactiveInfraEdges. It tombstones structural (non-behavioural) edges that
// this build did not re-stamp, restricted to edges this build had authority
// over.
//
// "Authority" is deliberately NOT edge.cloud_account_id alone. Cross-account
// rules stamp the forward edge with the destination node's account and the
// reverse edge with the source node's, and the cross-source enrichers stamp
// inconsistently (the LB→target edge carries the AWS account while the
// synthesized Service node carries the K8s one). Keying on the stamp alone would
// both miss edges that this build invalidated and sweep edges it never saw. The
// predicate therefore admits an edge when its stamp OR either of its endpoint
// nodes belongs to a rebuilt account.
//
// Edges with a NULL source (rows predating the edge `source` column) are never
// matched here; the tenant-wide sweep still handles them.
func (s *Service) markInactiveInfraEdgesScoped(
	tenantID string,
	syncVersion int64,
	accountIDs []string,
	ranSources []string,
) (int64, error) {
	if len(accountIDs) == 0 {
		return 0, fmt.Errorf("scoped edge sweep requires at least one account id")
	}
	if len(ranSources) == 0 {
		s.logger.Info("kg: skipping scoped infra edge sweep, no producers ran",
			"tenant_id", tenantID,
			"sync_version", syncVersion)
		return 0, nil
	}

	query := `
		UPDATE knowledge_graph_edge e
		SET is_active = false
		WHERE e.tenant_id = $1
		  AND e.last_sync_version < $2
		  AND e.level = 'Tenant'
		  AND e.is_active = true
		  AND e.relationship_type != ALL($3::text[])
		  AND e.source = ANY($4::text[])
		  AND (
		        e.cloud_account_id = ANY($5::uuid[])
		     OR EXISTS (
		          SELECT 1 FROM knowledge_graph_node n
		          WHERE n.id = e.source_node_id AND n.cloud_account_id = ANY($5::uuid[])
		        )
		     OR EXISTS (
		          SELECT 1 FROM knowledge_graph_node n
		          WHERE n.id = e.destination_node_id AND n.cloud_account_id = ANY($5::uuid[])
		        )
		  )
	`

	behavioralTypes := behavioralEdgeTypesSlice()

	result, err := s.dbManager.Exec(query,
		tenantID,
		syncVersion,
		pq.Array(behavioralTypes),
		pq.Array(ranSources),
		pq.Array(accountIDs),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to mark inactive infra edges (scoped): %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf(errRowsAffected, err)
	}

	s.logger.Info("kg: marked inactive infra edges (scoped)",
		"tenant_id", tenantID,
		"sync_version", syncVersion,
		"account_ids", accountIDs,
		"ran_sources", ranSources,
		"behavioral_types_excluded", behavioralTypes,
		"rows", rowsAffected)

	return rowsAffected, nil
}
