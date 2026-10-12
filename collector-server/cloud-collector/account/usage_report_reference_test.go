package account

// Reference implementation for the SQL derivation tests: the Go spend and
// billing-resource logic the cost sync used before it moved to SQL (#38972),
// copied unchanged. usage_report_derive_test.go checks the SQL output against
// it on the CUR fixture.

import (
	"fmt"
	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/providers"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
)

type referenceSpendRow struct {
	ID               string
	Date             string
	Amount           float64
	Unit             string
	CloudResourceID  *string
	ExcludeAggregate bool
	Tags             string
}

func referenceBuildSpendMap(account providers.Account, usageReport providers.GetUsageReportResponse, externalResourceIdMap map[string]string) (map[string]*referenceSpendRow, error) {
	spendMap := map[string]*referenceSpendRow{}
	var nilString *string

	for _, item := range usageReport.Items {
		externalResourceId := ""
		if item.ResourceId != "" {
			externalResourceId = buildExternalResourceId(account.CloudProvider, account.AccountNumber, item.ResourceRegionCode, item.ProductCode, item.ResourceType, item.ResourceId, "")
		}
		resourceId := externalResourceIdMap[externalResourceId]
		// spendKey must align with unique constraint (tenant, cloud_account, cloud_resource_id, date).
		// Credit/Refund items have ResourceId="" (NULL in DB) so they won't conflict with non-credit rows.
		// Use "credit" bucket to prevent credit costs merging with non-credit empty-resource rows.
		creditBucket := ""
		if item.CostCategory == "Credit" || item.CostCategory == "Refund" {
			creditBucket = "credit"
		}
		// Build spendKey to match DB unique constraint granularity.
		// For non-credit items: use externalResourceId which includes (provider, account, region,
		// service, resourceType, resourceId) — prevents cross-service collision when multiple
		// services share the same ResourceId (e.g., GCP services with NULL resource.name
		// all fall back to project ID).
		// For credit items: ResourceId is cleared to "", so use ProductCode+region to preserve
		// per-service credit attribution instead of collapsing all credits into one row per day.
		var spendKey string
		if creditBucket == "credit" {
			spendKey = fmt.Sprintf("credit:%s:%s:%s", item.ProductCode, item.ResourceRegionCode, item.StartDate.UTC().Format("2006-01-02"))
		} else {
			keyPart := externalResourceId
			if keyPart == "" {
				keyPart = fmt.Sprintf("%s:%s", item.ProductCode, item.ResourceRegionCode)
			}
			spendKey = fmt.Sprintf(":%s:%s", keyPart, item.StartDate.UTC().Format("2006-01-02"))
		}
		if _, ok := spendMap[spendKey]; !ok {
			tagsStr := []byte("{}")
			tags := item.ResourceTags
			tags["nb_service_name"] = []string{item.ProductCode}
			tags["nb_resource_type"] = []string{item.ResourceType}
			tags["nb_resource_region_code"] = []string{item.ResourceRegionCode}
			tags["nb_resource_id"] = []string{resourceId}
			if len(item.ResourceTags) > 0 {
				var err error
				tagsStr, err = common.MarshalJson(item.ResourceTags)
				if err != nil {
					return nil, fmt.Errorf("unable to marshal tags: %w", err)
				}
			}
			spendMap[spendKey] = &referenceSpendRow{
				ID:               uuid.New().String(),
				Date:             item.StartDate.UTC().Format("2006-01-02"),
				Amount:           item.Cost,
				Unit:             item.CostCurrency,
				CloudResourceID:  lo.Ternary(resourceId == "", nilString, &resourceId),
				ExcludeAggregate: creditBucket == "credit",
				Tags:             string(tagsStr),
			}
		} else {
			spendMap[spendKey].Amount += item.Cost
		}
	}
	return spendMap, nil
}

// referenceBillingResources is the per-resource row the sync upserted, keyed by
// external_resource_id (first line of a resource wins).
func referenceBillingResources(account providers.Account, accountId, tenantId string, usageReport providers.GetUsageReportResponse) (map[string]map[string]any, error) {
	resourceMap := map[string]map[string]any{}
	t := time.Now().UTC().Format(time.RFC3339)
	var err error
	var nilString *string
	for _, item := range usageReport.Items {
		if item.ResourceId == "" {
			continue
		}
		resourceMapKey := buildExternalResourceId(account.CloudProvider, account.AccountNumber, item.ResourceRegionCode, item.ProductCode, item.ResourceType, item.ResourceId, "")
		if _, ok := resourceMap[resourceMapKey]; ok {
			continue
		}
		tagsStr := []byte("{}")
		if len(item.ResourceTags) > 0 {
			tagsStr, err = common.MarshalJson(item.ResourceTags)
			if err != nil {
				return nil, err
			}
		}
		resourceDbData := map[string]any{
			"id":                   uuid.New().String(),
			"created_at":           t,
			"created_by":           nilString,
			"updated_at":           t,
			"updated_by":           nilString,
			"resourse_id":          item.ResourceId,
			"name":                 lo.Ternary(item.ResourceName != "", item.ResourceName, item.ResourceId),
			"type":                 item.ResourceType,
			"status":               providers.ResourceStatusActive,
			"resourse_created_on":  t,
			"account":              accountId,
			"cloud_provider":       account.CloudProvider,
			"region":               lo.Ternary(item.ResourceRegionCode == "", "global", item.ResourceRegionCode),
			"arn":                  item.ResourceArn,
			"tenant":               lo.Ternary(tenantId == "", nilString, &tenantId),
			"tags":                 string(tagsStr),
			"meta":                 `{"nb_source":"billing"}`,
			"service_name":         item.ProductCode,
			"first_seen":           t,
			"last_seen":            t,
			"is_active":            true,
			"external_resource_id": resourceMapKey,
		}

		resourceMap[resourceMapKey] = resourceDbData
	}

	return resourceMap, nil
}
