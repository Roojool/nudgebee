package account

import (
	"fmt"
	"strings"
	"time"

	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/providers"
	"nudgebee/collector/cloud/security"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/samber/lo"
)

// The cost sync never holds a month's report in memory. Line items are streamed
// into cloud_account_usage_report, tagged with the run's report_inserted_date,
// and billing resources and spends are derived from those rows in SQL. Memory
// is one batch, whatever the size of the bill (#38972).
//
// Rows of the run are matched by (account_id, report_date, report_inserted_date);
// the (account_id, report_date) index serves every query here.

// usageReportSink writes a sync run's line items to cloud_account_usage_report.
type usageReportSink struct {
	ctx        *security.RequestContext
	dbms       *common.DatabaseManager
	accountId  string
	account    providers.Account
	tenantId   *string
	reportDate time.Time
	runMarker  time.Time
	lineNo     int64
	// err is the first write failure, so the caller can tell a database error
	// from a provider error in what the stream returns.
	err error
}

func newUsageReportSink(ctx *security.RequestContext, dbms *common.DatabaseManager, accountId string, account providers.Account, month time.Month, year int) *usageReportSink {
	var tenantId *string
	if t := ctx.GetSecurityContext().GetTenantId(); t != "" {
		tenantId = &t
	}
	return &usageReportSink{
		ctx:        ctx,
		dbms:       dbms,
		accountId:  accountId,
		account:    account,
		tenantId:   tenantId,
		reportDate: time.Date(year, month, 1, 0, 0, 0, 0, time.UTC),
		// timestamp columns keep microseconds; truncate so the marker compares equal.
		runMarker: time.Now().UTC().Truncate(time.Microsecond),
	}
}

const usageReportInsertQuery = `INSERT INTO cloud_account_usage_report (id, tenant_id, account_id, report_date, report_inserted_date, product_code, product_service_code, resource_region_code, resource_id, resource_type, resource_arn, cost_category, cost_sub_category, resource_operation, cost, cost_currency, resource_tags, start_date, end_date, external_resource_id, resource_name, line_no)
	VALUES (:id, :tenant_id, :account_id, :report_date, :report_inserted_date, :product_code, :product_service_code, :resource_region_code, :resource_id, :resource_type, :resource_arn, :cost_category, :cost_sub_category, :resource_operation, :cost, :cost_currency, :resource_tags, :start_date, :end_date, :external_resource_id, :resource_name, :line_no)`

// write stores one batch. 22 columns x 1000 rows stays under PostgreSQL's
// 65535 bind-parameter limit.
func (s *usageReportSink) write(batch []providers.UsageReportItem) error {
	rows := make([]map[string]any, 0, len(batch))
	for _, item := range batch {
		if err := common.ValidateStruct(item); err != nil {
			s.ctx.GetLogger().Error("usagereport: invalid usage report item", "error", err, "data", item)
			s.err = err
			return err
		}
		tags := "{}"
		if len(item.ResourceTags) != 0 {
			tagsJson, err := common.MarshalJson(item.ResourceTags)
			if err != nil {
				s.ctx.GetLogger().Error("usagereport: unable to marshal tags", "error", err)
				s.err = err
				return err
			}
			tags = string(tagsJson)
		}
		// The identity spends and billing resources are matched on, built from
		// the raw region exactly as the resource sync builds it.
		var externalResourceId *string
		if item.ResourceId != "" {
			e := buildExternalResourceId(s.account.CloudProvider, s.account.AccountNumber, item.ResourceRegionCode, item.ProductCode, item.ResourceType, item.ResourceId, "")
			externalResourceId = &e
		}
		s.lineNo++
		rows = append(rows, map[string]any{
			"id":                   uuid.New().String(),
			"tenant_id":            s.tenantId,
			"account_id":           s.accountId,
			"report_date":          s.reportDate.Format("2006-01-02"),
			"report_inserted_date": s.runMarker,
			"product_code":         item.ProductCode,
			"product_service_code": item.ProductServiceCode,
			"resource_region_code": lo.Ternary(item.ResourceRegionCode == "", "global", item.ResourceRegionCode),
			"resource_id":          item.ResourceId,
			"resource_type":        item.ResourceType,
			"resource_arn":         item.ResourceArn,
			"cost_category":        item.CostCategory,
			"cost_sub_category":    item.CostSubCategory,
			"resource_operation":   item.ResourceOperation,
			"cost":                 item.Cost,
			"cost_currency":        lo.Ternary(item.CostCurrency == "", "USD", item.CostCurrency),
			"resource_tags":        tags,
			// UTC, so start_date::date is the UTC day the spend row is dated by.
			"start_date":           item.StartDate.UTC(),
			"end_date":             item.EndDate.UTC(),
			"external_resource_id": externalResourceId,
			"resource_name":        item.ResourceName,
			"line_no":              s.lineNo,
		})
	}
	if _, err := s.dbms.NamedExec(usageReportInsertQuery, rows, common.InsertQueryOption{InertBatchSize: 1000}); err != nil {
		s.ctx.GetLogger().Error("usagereport: unable to insert usage report rows", "error", err, "line_no", s.lineNo)
		s.err = err
		return err
	}
	return nil
}

func (s *usageReportSink) count() int {
	return int(s.lineNo)
}

// replacePreviousRuns deletes the month's rows written by earlier runs, once
// this run's rows are all stored. Rows from before the run marker existed
// have a NULL report_inserted_date, hence IS DISTINCT FROM.
func (s *usageReportSink) replacePreviousRuns() error {
	res, err := s.dbms.Exec(`DELETE FROM cloud_account_usage_report
		WHERE account_id = $1 AND report_date >= $2 AND report_date < $3 AND report_inserted_date IS DISTINCT FROM $4`,
		s.accountId, s.reportDate, s.reportDate.AddDate(0, 1, 0), s.runMarker)
	if err != nil {
		s.ctx.GetLogger().Error("usagereport: unable to delete previous usage report", "error", err)
		return err
	}
	if n, err := res.RowsAffected(); err == nil {
		s.ctx.GetLogger().Info("usagereport: deleted previous usage report", "count", n)
	}
	return nil
}

// discard deletes this run's rows after a failed stream, so the stored month
// stays the last complete one. A run killed mid-stream leaves its rows behind;
// the next successful run deletes them in replacePreviousRuns.
func (s *usageReportSink) discard() {
	if s.lineNo == 0 {
		return
	}
	if _, err := s.dbms.Exec(`DELETE FROM cloud_account_usage_report WHERE account_id = $1 AND report_date = $2 AND report_inserted_date = $3`,
		s.accountId, s.reportDate, s.runMarker); err != nil {
		s.ctx.GetLogger().Error("usagereport: unable to delete rows of failed run", "error", err)
	}
}

// runRows filters cloud_account_usage_report to the run: $1 account, $2 report
// date, $3 run marker.
const runRows = `account_id = $1 AND report_date = $2 AND report_inserted_date = $3`

// deriveBillingResources upserts one cloud_resourses row per resource billed in
// the run and retires billing-sourced rows the run no longer bills. First line
// of a resource wins for its name, tags and arn.
//
// The month's rows are sorted into a temp table before any cloud_resourses
// statement, so the row locks are held only for the writes, not the sort.
func deriveBillingResources(ctx *security.RequestContext, dbms *common.DatabaseManager, accountId string, account providers.Account, s *usageReportSink) (int, error) {
	t := time.Now().UTC().Format(time.RFC3339)
	var staged int64
	var stagedIn, wroteIn time.Duration
	_, err := dbms.DoInTransaction(func(tx common.DatabaseManagerTx) (any, error) {
		t0 := time.Now()
		if _, err := tx.Exec(`CREATE TEMP TABLE nb_billing_resource_stage (
				external_resource_id text, resource_id text, resource_name text, resource_type text,
				resource_region_code text, resource_arn text, product_code text, resource_tags jsonb
			) ON COMMIT DROP`); err != nil {
			return nil, fmt.Errorf("create resource stage: %w", err)
		}
		res, err := tx.Exec(`INSERT INTO nb_billing_resource_stage
			SELECT DISTINCT ON (external_resource_id)
				external_resource_id, resource_id, resource_name, resource_type,
				resource_region_code, resource_arn, product_code, resource_tags
			FROM cloud_account_usage_report
			WHERE `+runRows+` AND external_resource_id IS NOT NULL
			ORDER BY external_resource_id, line_no`, accountId, s.reportDate, s.runMarker)
		if err != nil {
			return nil, fmt.Errorf("stage resources: %w", err)
		}
		if staged, err = res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("stage resources: %w", err)
		}
		if staged == 0 {
			return nil, nil
		}
		// Temp tables are never auto-analyzed; without stats the planner can
		// pick a nested loop over hundreds of thousands of staged rows.
		if _, err := tx.Exec(`ANALYZE nb_billing_resource_stage`); err != nil {
			return nil, fmt.Errorf("analyze resource stage: %w", err)
		}
		stagedIn = time.Since(t0)
		t0 = time.Now()
		defer func() { wroteIn = time.Since(t0) }()

		// Retire billing-sourced rows this run did not bill. NULL ids are left
		// alone, as the earlier != ALL(keys) form did.
		if _, err := tx.Exec(`UPDATE cloud_resourses SET is_active = false, last_seen = $2, status = $3
			WHERE account = $1 AND status != 'Deleted' AND meta->>'nb_source' = 'billing'
				AND external_resource_id IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM nb_billing_resource_stage s WHERE s.external_resource_id = cloud_resourses.external_resource_id)`,
			accountId, t, providers.ResourceStatusDeleted); err != nil {
			return nil, fmt.Errorf("update resources inactive: %w", err)
		}
		if !isAzure(account) {
			// Reconcile only resources whose id is not stored yet — the reconcile
			// skips the rest anyway. Joined over every staged resource, the
			// planner picks a per-resource index lookup that reads tens of
			// thousands of index entries each (30 s for ~1k resources on an
			// account with ~150k rows); on a steady account this set is empty.
			if _, err := tx.Exec(`CREATE TEMP TABLE nb_billing_reconcile_stage (
					resourse_id text, type text, region text, service_name text, erid text
				) ON COMMIT DROP`); err != nil {
				return nil, fmt.Errorf("create reconcile stage: %w", err)
			}
			res, err := tx.Exec(`INSERT INTO nb_billing_reconcile_stage (resourse_id, type, region, service_name, erid)
				SELECT s.resource_id, s.resource_type, s.resource_region_code, s.product_code, s.external_resource_id
				FROM nb_billing_resource_stage s
				WHERE s.resource_id <> '' AND NOT EXISTS (
					SELECT 1 FROM cloud_resourses g WHERE g.account = $1 AND g.external_resource_id = s.external_resource_id)`, accountId)
			if err != nil {
				return nil, fmt.Errorf("stage reconcile: %w", err)
			}
			if unmatched, err := res.RowsAffected(); err != nil {
				return nil, fmt.Errorf("stage reconcile: %w", err)
			} else if unmatched > 0 {
				if _, err := tx.Exec(`ANALYZE nb_billing_reconcile_stage`); err != nil {
					return nil, fmt.Errorf("analyze reconcile stage: %w", err)
				}
				if _, err := tx.Exec(reconcileExternalResourceIdsQuery(`SELECT resourse_id, type, region, service_name, erid FROM nb_billing_reconcile_stage`), accountId); err != nil {
					return nil, fmt.Errorf("reconcile external_resource_id: %w", err)
				}
			}
		}
		if _, err := tx.Exec(`INSERT INTO cloud_resourses (id, created_at, created_by, updated_at, updated_by, resourse_id, name, type, status, resourse_created_on, account, cloud_provider, region, arn, tenant, tags, meta, service_name, first_seen, last_seen, is_active, external_resource_id)
			SELECT gen_random_uuid(), $2::timestamp, NULL::uuid, $2::timestamp, NULL::uuid, s.resource_id, COALESCE(NULLIF(s.resource_name, ''), s.resource_id), s.resource_type, $3::text, $2::timestamp,
				$1::uuid, $4::text, s.resource_region_code, s.resource_arn, $5::uuid, s.resource_tags, '{"nb_source":"billing"}'::jsonb, s.product_code, $2::timestamp, $2::timestamp, true, s.external_resource_id
			FROM nb_billing_resource_stage s
			ORDER BY s.external_resource_id`+billingResourceConflictClause(account),
			accountId, t, providers.ResourceStatusActive, account.CloudProvider, s.tenantId); err != nil {
			return nil, fmt.Errorf("upsert resources: %w", err)
		}
		return nil, nil
	})
	if err != nil {
		ctx.GetLogger().Error("usagereport: unable to update resources", "error", err)
		return 0, err
	}
	// wrote is how long cloud_resourses rows stayed locked.
	ctx.GetLogger().Info("usagereport: derived billing resources", "count", staged, "staged", stagedIn.String(), "wrote", wroteIn.String())
	return int(staged), nil
}

func isAzure(account providers.Account) bool {
	return strings.EqualFold(account.CloudProvider, "azure")
}

// billingResourceConflictClause: Azure arbitrates on (account,
// external_resource_id) and may rewrite the natural-key columns because its
// external_resource_id is the full resource path. AWS/GCP arbitrate on the same
// pair, paired with the reconcile step, but never rewrite the natural key: an
// external_resource_id match can land on a row whose 5-column key differs, and
// rewriting it could violate the 5-column unique index.
func billingResourceConflictClause(account providers.Account) string {
	if isAzure(account) {
		return `
			ON CONFLICT (account, external_resource_id) DO UPDATE SET
				last_seen = EXCLUDED.last_seen,
				tags = cloud_resourses.tags || EXCLUDED.tags,
				meta = CASE
					WHEN COALESCE(cloud_resourses.meta->>'nb_source', '') = ''
						THEN COALESCE(cloud_resourses.meta, CAST('{}' AS jsonb)) || EXCLUDED.meta
					ELSE
						COALESCE(cloud_resourses.meta, CAST('{}' AS jsonb)) || (EXCLUDED.meta - 'nb_source')
				END,
				arn = EXCLUDED.arn,
				resourse_id = EXCLUDED.resourse_id,
				name = EXCLUDED.name,
				type = EXCLUDED.type,
				region = EXCLUDED.region,
				service_name = EXCLUDED.service_name,
				is_active = EXCLUDED.is_active,
				status = EXCLUDED.status`
	}
	return `
			ON CONFLICT (account, external_resource_id) DO UPDATE SET
				last_seen = EXCLUDED.last_seen,
				tags = cloud_resourses.tags || EXCLUDED.tags,
				meta = CASE
					WHEN COALESCE(cloud_resourses.meta->>'nb_source', '') = ''
						THEN COALESCE(cloud_resourses.meta, CAST('{}' AS jsonb)) || EXCLUDED.meta
					ELSE
						COALESCE(cloud_resourses.meta, CAST('{}' AS jsonb)) || (EXCLUDED.meta - 'nb_source')
				END,
				arn = EXCLUDED.arn,
				name = EXCLUDED.name,
				is_active = EXCLUDED.is_active,
				status = EXCLUDED.status`
}

// deriveSpends writes one spends row per (spend key, day) of the run, in one
// transaction as before:
//   - key: credits/refunds by product+region (one row per service, not per
//     resource); other lines by resource; lines without a resource by
//     product+region.
//   - amount: sum over the key's lines; unit, tags and cloud_resource_id from
//     its first line, tags extended with the nb_* keys.
//   - rows with a resource are upserted; rows without one replace the
//     account's NULL-resource rows for the run's days.
//
// The aggregate is staged before the writes, so spends row locks are held only
// for the writes.
func deriveSpends(ctx *security.RequestContext, dbms *common.DatabaseManager, accountId string, s *usageReportSink) (int, error) {
	var staged int64
	var stagedIn, wroteIn time.Duration
	_, err := dbms.DoInTransaction(func(tx common.DatabaseManagerTx) (any, error) {
		t0 := time.Now()
		if _, err := tx.Exec(`CREATE TEMP TABLE nb_spend_stage (
				d date, amount double precision, unit text, cloud_resource_id uuid, exclude_aggregate boolean, tags jsonb
			) ON COMMIT DROP`); err != nil {
			return nil, fmt.Errorf("create spend stage: %w", err)
		}
		res, err := tx.Exec(`INSERT INTO nb_spend_stage
			SELECT DISTINCT ON (k.spend_key, k.d)
				k.d,
				SUM(k.cost) OVER (PARTITION BY k.spend_key, k.d),
				k.cost_currency,
				k.cloud_resource_id,
				k.is_credit,
				k.resource_tags || jsonb_build_object(
					'nb_service_name', jsonb_build_array(k.product_code),
					'nb_resource_type', jsonb_build_array(k.resource_type),
					'nb_resource_region_code', jsonb_build_array(k.resource_region_code),
					'nb_resource_id', jsonb_build_array(COALESCE(k.cloud_resource_id::text, '')))
			FROM (
				SELECT u.line_no, u.cost, u.cost_currency, u.resource_tags, u.product_code, u.resource_type,
					u.resource_region_code, u.start_date::date AS d,
					-- Credits and refunds are kept per product and region, never per
					-- resource (the AWS and GCP parsers already clear the resource on
					-- them); a resource here would make them collide with that
					-- resource's usage row and be dropped.
					CASE WHEN u.cost_category IN ('Credit', 'Refund') THEN NULL ELSE c.id END AS cloud_resource_id,
					u.cost_category IN ('Credit', 'Refund') AS is_credit,
					CASE
						WHEN u.cost_category IN ('Credit', 'Refund') THEN 'credit:' || u.product_code || ':' || u.resource_region_code
						WHEN u.external_resource_id IS NOT NULL THEN ':' || u.external_resource_id
						ELSE ':' || u.product_code || ':' || u.resource_region_code
					END AS spend_key
				FROM cloud_account_usage_report u
				LEFT JOIN cloud_resourses c ON c.account = $1 AND c.external_resource_id = u.external_resource_id
				WHERE u.`+runRows+`
			) k
			ORDER BY k.spend_key, k.d, k.line_no`, accountId, s.reportDate, s.runMarker)
		if err != nil {
			return nil, fmt.Errorf("stage spends: %w", err)
		}
		if staged, err = res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("stage spends: %w", err)
		}
		if _, err := tx.Exec(`ANALYZE nb_spend_stage`); err != nil {
			return nil, fmt.Errorf("analyze spend stage: %w", err)
		}
		stagedIn = time.Since(t0)
		t0 = time.Now()
		defer func() { wroteIn = time.Since(t0) }()

		if _, err := tx.Exec(`INSERT INTO spends (id, date, amount, unit, business_unit, tenant, cloud_account, cloud_resource_id, exclude_aggregate, tags)
			SELECT gen_random_uuid(), d, amount, unit, NULL::uuid, $2::uuid, $1::uuid, cloud_resource_id, exclude_aggregate, tags
			FROM nb_spend_stage WHERE cloud_resource_id IS NOT NULL
			ON CONFLICT (tenant, cloud_account, cloud_resource_id, date)
			DO UPDATE SET amount = EXCLUDED.amount, unit = EXCLUDED.unit, tags = EXCLUDED.tags, exclude_aggregate = EXCLUDED.exclude_aggregate`,
			accountId, s.tenantId); err != nil {
			return nil, fmt.Errorf("upsert spends: %w", err)
		}
		// NULL cloud_resource_id rows cannot be upserted: NULLs are distinct in
		// the unique index, so ON CONFLICT never matches them.
		if _, err := tx.Exec(`DELETE FROM spends WHERE cloud_account = $1 AND cloud_resource_id IS NULL
			AND date IN (SELECT DISTINCT d FROM nb_spend_stage)`, accountId); err != nil {
			return nil, fmt.Errorf("delete null-resource spends: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO spends (id, date, amount, unit, business_unit, tenant, cloud_account, cloud_resource_id, exclude_aggregate, tags)
			SELECT gen_random_uuid(), d, amount, unit, NULL::uuid, $2::uuid, $1::uuid, NULL::uuid, exclude_aggregate, tags
			FROM nb_spend_stage WHERE cloud_resource_id IS NULL`, accountId, s.tenantId); err != nil {
			return nil, fmt.Errorf("insert null-resource spends: %w", err)
		}
		return nil, nil
	})
	if err != nil {
		ctx.GetLogger().Error("usagereport: unable to update spends", "error", err)
		return 0, err
	}
	// wrote is how long spends rows stayed locked.
	ctx.GetLogger().Info("usagereport: derived spends", "count", staged, "staged", stagedIn.String(), "wrote", wroteIn.String())
	return int(staged), nil
}

// dailySpendSummary holds the figures of the daily cost notification.
// Credit/Refund lines carry negative costs and are excluded, so the reported
// spend is gross rather than net-of-credits — matching the exclude_aggregate
// filter applied everywhere spends are aggregated for display. Without this,
// accounts whose credits meet or exceed usage show zero or negative totals
// (issue #29455).
type dailySpendSummary struct {
	totalDay        float64
	totalMonth      float64
	currency        string
	mixedCurrencies bool
	// top 5 lines of the day by cost: cost, product_code, resource_arn
	topDailyItems []map[string]any
	// top 5 services of the month by cost: service, cost
	topMonthlyServices []map[string]any
}

// summarizeDailySpend computes the notification figures from the run's rows,
// anchored on day (the latest day billing data exists for) by line end date.
func summarizeDailySpend(dbms *common.DatabaseManager, accountId string, s *usageReportSink, day time.Time) (dailySpendSummary, error) {
	summary := dailySpendSummary{}
	dayStr := day.UTC().Format("2006-01-02")
	const usage = ` AND cost_category NOT IN ('Credit', 'Refund')`
	totals := struct {
		TotalDay   float64        `db:"total_day"`
		TotalMonth float64        `db:"total_month"`
		Currencies pq.StringArray `db:"currencies"`
	}{}
	if err := dbms.QueryRowAndScan(&totals, `SELECT
			COALESCE(SUM(cost) FILTER (WHERE end_date::date = $4::date), 0) AS total_day,
			COALESCE(SUM(cost) FILTER (WHERE date_trunc('month', end_date) = date_trunc('month', $4::date)), 0) AS total_month,
			COALESCE((SELECT array_agg(c ORDER BY first_line) FROM (
				SELECT cost_currency AS c, MIN(line_no) AS first_line FROM cloud_account_usage_report
				WHERE `+runRows+usage+` AND cost_currency <> '' GROUP BY cost_currency) cur), '{}') AS currencies
		FROM cloud_account_usage_report WHERE `+runRows+usage, accountId, s.reportDate, s.runMarker, dayStr); err != nil {
		return summary, fmt.Errorf("summarize spend: %w", err)
	}
	summary.totalDay = totals.TotalDay
	summary.totalMonth = totals.TotalMonth
	if len(totals.Currencies) > 0 {
		summary.currency = totals.Currencies[0]
		summary.mixedCurrencies = len(totals.Currencies) > 1
	}

	daily := []struct {
		Cost        float64 `db:"cost"`
		ProductCode string  `db:"product_code"`
		ResourceArn *string `db:"resource_arn"`
	}{}
	if err := dbms.QueryAndScan(&daily, `SELECT cost, product_code, resource_arn FROM cloud_account_usage_report
		WHERE `+runRows+usage+` AND end_date::date = $4::date ORDER BY cost DESC, line_no LIMIT 5`,
		accountId, s.reportDate, s.runMarker, dayStr); err != nil {
		return summary, fmt.Errorf("summarize daily items: %w", err)
	}
	summary.topDailyItems = make([]map[string]any, 0, len(daily))
	for _, d := range daily {
		summary.topDailyItems = append(summary.topDailyItems, map[string]any{
			"cost":         d.Cost,
			"product_code": d.ProductCode,
			"resource_arn": lo.FromPtr(d.ResourceArn),
		})
	}

	services := []struct {
		Service string  `db:"service"`
		Cost    float64 `db:"cost"`
	}{}
	if err := dbms.QueryAndScan(&services, `SELECT product_code AS service, SUM(cost) AS cost FROM cloud_account_usage_report
		WHERE `+runRows+usage+` AND date_trunc('month', end_date) = date_trunc('month', $4::date)
		GROUP BY product_code ORDER BY cost DESC, product_code LIMIT 5`,
		accountId, s.reportDate, s.runMarker, dayStr); err != nil {
		return summary, fmt.Errorf("summarize monthly services: %w", err)
	}
	summary.topMonthlyServices = make([]map[string]any, 0, len(services))
	for _, sv := range services {
		summary.topMonthlyServices = append(summary.topMonthlyServices, map[string]any{
			"service": sv.Service,
			"cost":    sv.Cost,
		})
	}
	return summary, nil
}
