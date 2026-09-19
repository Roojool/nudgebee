package resourcepatch

import (
	"log/slog"
	"time"

	"nudgebee/services/config"
	"nudgebee/services/internal/database"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// store is the graph persistence and inventory row access the applier needs.
// Resource-type knowledge lives in the policies, not here. dbStore is the
// production implementation; tests substitute a fake.
type store interface {
	FindActiveNodesByProviderID(tenantID, accountID, providerID string) ([]*core.StoredNode, error)
	FindActiveNodeByID(tenantID, nodeID string) (*core.StoredNode, error)
	FindActiveNodesByName(tenantID, accountID, source, specificType, name string) ([]*core.StoredNode, error)
	TombstoneNodeWithIncidentEdges(tenantID, nodeID string) (int64, error)
	TombstoneOutgoingEdges(tenantID, nodeID string, sig core.EdgeSignature, syncVersion int64) (int64, error)
	LoadActiveNodesByTypes(tenantID, accountID string, types []core.NodeType) ([]*core.DbNode, error)
	CurrentSyncVersion(tenantID string) (int64, error)
	ExcludeBuilds(tenantID string, fn func(runningCovers func(accountID string) (bool, error)) error) error
	SaveNodes(nodes []*core.DbNode, syncVersion int64) error
	SaveEdges(edges []*core.DbEdge, nodes []*core.DbNode, syncVersion int64) error
	LoadCloudResourceRows(accountID, resourceID, rowID string) ([]sources.CloudResourceRow, error)
}

// dbStore is the metastore-backed store.
type dbStore struct {
	*core.ResourcePatchRepository
	requests *core.BuildRequestRepository
	service  *core.Service
}

func newDBStore(dbManager *database.DatabaseManager, logger *slog.Logger) *dbStore {
	return &dbStore{
		ResourcePatchRepository: core.NewResourcePatchRepository(dbManager, logger),
		requests: core.NewBuildRequestRepository(dbManager, logger).WithPolicy(core.BuildRequestPolicy{
			StaleAfter: time.Duration(config.Config.KGUpdateProcessingTimeoutMinutes) * time.Minute,
		}),
		service: core.NewService(nil, logger, dbManager),
	}
}

func (s *dbStore) ExcludeBuilds(tenantID string, fn func(runningCovers func(accountID string) (bool, error)) error) error {
	return s.requests.ExcludeBuilds(tenantID, fn)
}

func (s *dbStore) SaveNodes(nodes []*core.DbNode, syncVersion int64) error {
	return s.service.SaveNodes(nodes, syncVersion)
}

func (s *dbStore) SaveEdges(edges []*core.DbEdge, nodes []*core.DbNode, syncVersion int64) error {
	return s.service.SaveEdges(edges, nodes, syncVersion)
}

func (s *dbStore) LoadCloudResourceRows(accountID, resourceID, rowID string) ([]sources.CloudResourceRow, error) {
	return sources.LoadActiveCloudResourceRows(accountID, resourceID, rowID)
}
