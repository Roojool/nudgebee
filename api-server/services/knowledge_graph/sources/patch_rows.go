package sources

import (
	"fmt"

	"github.com/google/uuid"

	"nudgebee/services/internal/database"
)

// LoadActiveCloudResourceRows reads the account's active cloud_resourses rows
// for one resource, with the projection the cloud sources build from. A resource
// can have several rows (AWS reports an EC2 instance once from EC2 describe and
// once from Systems Manager, under the same resource id), so rows are matched by
// the provider resource id or by the row id (nb_resource_id) and all matches are
// returned. Either id may be empty. Served by the (account, resourse_id, ...)
// unique index and the primary key.
func LoadActiveCloudResourceRows(accountID, resourceID, nbResourceID string) ([]CloudResourceRow, error) {
	if accountID == "" || (resourceID == "" && nbResourceID == "") {
		return nil, fmt.Errorf("account id and a resource id are required")
	}
	// An empty id must match nothing (some rows have an empty resourse_id) and a
	// non-uuid row id must not fail the cast, so both are passed as NULL.
	var providerID, rowID *string
	if resourceID != "" {
		providerID = &resourceID
	}
	if _, err := uuid.Parse(nbResourceID); err == nil {
		rowID = &nbResourceID
	}

	dbManager, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, fmt.Errorf("failed to get database manager: %w", err)
	}
	var rows []CloudResourceRow
	if err := dbManager.Db.Select(&rows, `
		SELECT
			cr.id, cr.resourse_id, cr.name, cr.type, cr.status, cr.account, cr.tenant,
			cr.cloud_provider, cr.region, COALESCE(cr.arn, '') AS arn, cr.tags, cr.meta, cr.service_name,
			cr.is_active, cr.external_resource_id,
			ca.account_number
		FROM cloud_resourses cr
		LEFT JOIN cloud_accounts ca ON cr.account = ca.id
		WHERE cr.account = $1
		  AND cr.is_active = true
		  AND (cr.resourse_id = $2 OR cr.id = $3::uuid)
		ORDER BY cr.type, cr.name
	`, accountID, providerID, rowID); err != nil {
		return nil, fmt.Errorf("failed to load cloud resource rows: %w", err)
	}
	return rows, nil
}
