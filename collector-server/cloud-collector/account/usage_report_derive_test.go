package account

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/config"
	"nudgebee/collector/cloud/providers"
	"nudgebee/collector/cloud/security"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cost sync derives billing resources and spends in SQL
// (usage_report_derive.go). These tests run that SQL against Postgres: set
// NB_TEST_POSTGRES_DSN (CI sets it, with NB_REQUIRE_POSTGRES=1 so a missing
// database fails instead of skipping). Each run creates its own schema.

var deriveTestDB struct {
	once sync.Once
	dbms *common.DatabaseManager
	err  error
}

const deriveTestSchemaDDL = `
CREATE TABLE cloud_account_usage_report (
	id uuid PRIMARY KEY, tenant_id uuid, account_id uuid, report_date timestamp, product_code text,
	product_service_code text, resource_region_code text, resource_id text, cost_category text,
	cost_sub_category text, resource_operation text, cost double precision, cost_currency text,
	resource_tags jsonb, start_date timestamp, end_date timestamp, raw jsonb, report_inserted_date timestamp,
	resource_type text, resource_arn text, external_resource_id text, resource_name text, line_no bigint);
CREATE INDEX idx_caur_account_report_date ON cloud_account_usage_report (account_id, report_date);
CREATE TABLE cloud_resourses (
	id uuid PRIMARY KEY, created_at timestamp, created_by uuid, updated_at timestamp, updated_by uuid,
	resourse_id text, name text, type text, status text, resourse_created_on timestamp, account uuid,
	cloud_provider text, region text, arn text, tenant uuid, tags jsonb, meta jsonb, service_name text,
	first_seen timestamp, last_seen timestamp, is_active boolean, external_resource_id text);
CREATE UNIQUE INDEX cloud_resourses_account_external_resource_id_key ON cloud_resourses (account, external_resource_id);
CREATE UNIQUE INDEX cloud_resourses_account_resourse_service_type_region_key ON cloud_resourses (account, resourse_id, type, region, service_name);
CREATE TABLE spends (
	id uuid PRIMARY KEY, date timestamp, amount double precision, unit text, business_unit uuid, tenant uuid,
	cloud_account uuid, cloud_resource_id uuid REFERENCES cloud_resourses (id), exclude_aggregate boolean, tags jsonb);
CREATE UNIQUE INDEX spends_tenant_cloud_account_cloud_resource_id_date_key ON spends (tenant, cloud_account, cloud_resource_id, date);
`

func deriveTestDatabase(t *testing.T) *common.DatabaseManager {
	t.Helper()
	dsn := os.Getenv("NB_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("NB_REQUIRE_POSTGRES") != "" {
			t.Fatal("NB_REQUIRE_POSTGRES is set but NB_TEST_POSTGRES_DSN is empty")
		}
		t.Skip("set NB_TEST_POSTGRES_DSN to run the cost-sync SQL tests against Postgres")
	}
	deriveTestDB.once.Do(func() {
		schema := fmt.Sprintf("nb_cost_sync_test_%d", time.Now().UnixNano())
		db, err := sqlx.Open("postgres", dsn)
		if err != nil {
			deriveTestDB.err = err
			return
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`CREATE SCHEMA ` + schema + `; SET search_path TO ` + schema + `;` + deriveTestSchemaDDL); err != nil {
			deriveTestDB.err = err
			return
		}
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		config.Config.CloudCollectorServerDBUrl = dsn + sep + "search_path=" + schema
		config.Config.CloudCollectorServerDBMaxConnection = 4
		config.Config.CloudCollectorServerDBMinConnection = 1
		config.Config.CloudCollectorServerDBIdleMinutes = 1
		deriveTestDB.dbms, deriveTestDB.err = common.GetDatabaseManager(common.Metastore)
	})
	require.NoError(t, deriveTestDB.err)
	return deriveTestDB.dbms
}

type deriveRun struct {
	ctx       *security.RequestContext
	dbms      *common.DatabaseManager
	account   providers.Account
	accountId string
	tenantId  string
}

func newDeriveRun(t *testing.T, provider, accountNumber string) *deriveRun {
	dbms := deriveTestDatabase(t)
	tenantId := uuid.New().String()
	return &deriveRun{
		ctx:       security.NewRequestContextForTenantAdmin(tenantId),
		dbms:      dbms,
		account:   providers.Account{CloudProvider: provider, AccountNumber: accountNumber},
		accountId: uuid.New().String(),
		tenantId:  tenantId,
	}
}

// sync runs the stages of StoreUsage after the provider fetch: stream items,
// replace earlier runs, derive resources (current-month runs only) and spends.
func (r *deriveRun) sync(t *testing.T, items []providers.UsageReportItem, withResources bool) *usageReportSink {
	t.Helper()
	sink := newUsageReportSink(r.ctx, r.dbms, r.accountId, r.account, time.April, 2026)
	for start := 0; start < len(items); start += providers.UsageReportBatchSize {
		require.NoError(t, sink.write(items[start:min(start+providers.UsageReportBatchSize, len(items))]))
	}
	require.NoError(t, sink.replacePreviousRuns())
	if withResources {
		_, err := deriveBillingResources(r.ctx, r.dbms, r.accountId, r.account, sink)
		require.NoError(t, err)
	}
	_, err := deriveSpends(r.ctx, r.dbms, r.accountId, sink)
	require.NoError(t, err)
	return sink
}

type spendTestRow struct {
	Date             time.Time `db:"date"`
	Amount           float64   `db:"amount"`
	Unit             string    `db:"unit"`
	CloudResourceID  *string   `db:"cloud_resource_id"`
	ExcludeAggregate bool      `db:"exclude_aggregate"`
	Tags             string    `db:"tags"`
}

func (r *deriveRun) spends(t *testing.T) []spendTestRow {
	t.Helper()
	rows := []spendTestRow{}
	require.NoError(t, r.dbms.QueryAndScan(&rows, `SELECT date, amount, unit, cloud_resource_id::text AS cloud_resource_id, exclude_aggregate, tags::text AS tags
		FROM spends WHERE cloud_account = $1`, r.accountId))
	return rows
}

func spendTag(t *testing.T, row spendTestRow, key string) string {
	t.Helper()
	var tags map[string][]string
	require.NoError(t, json.Unmarshal([]byte(row.Tags), &tags))
	require.Len(t, tags[key], 1, "tag %s", key)
	return tags[key][0]
}

var deriveTestDay = time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

func usageItem(product, resourceType, resourceId, region string, category providers.UsageReportCostCategory, cost float64) providers.UsageReportItem {
	return providers.UsageReportItem{
		ProductCode:        product,
		ProductServiceCode: product,
		ResourceType:       resourceType,
		ResourceId:         resourceId,
		ResourceRegionCode: region,
		CostCategory:       category,
		Cost:               cost,
		CostCurrency:       "INR",
		StartDate:          deriveTestDay,
		EndDate:            deriveTestDay.Add(24*time.Hour - time.Nanosecond),
		ResourceTags:       map[string][]string{},
	}
}

// GCP services with a NULL resource.name all fall back to the project id; they
// must stay one spend row per service, not collapse into one.
func TestDeriveSpends_GCPNullResourceName(t *testing.T) {
	r := newDeriveRun(t, "GCP", "test-cluster-dev")
	r.sync(t, []providers.UsageReportItem{
		usageItem("Artifact Registry", "artifact-registry", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 100.0),
		usageItem("BigQuery", "bigquery", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 0.78),
		usageItem("VM Manager", "vm-manager", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 168.0),
	}, true)

	rows := r.spends(t)
	require.Len(t, rows, 3)
	found := map[string]float64{}
	for _, row := range rows {
		require.NotNil(t, row.CloudResourceID)
		found[spendTag(t, row, "nb_service_name")] = row.Amount
	}
	assert.Equal(t, map[string]float64{"Artifact Registry": 100.0, "BigQuery": 0.78, "VM Manager": 168.0}, found)
}

// Credits are one row per service and region, excluded from aggregates.
func TestDeriveSpends_CreditsSeparatePerService(t *testing.T) {
	r := newDeriveRun(t, "GCP", "test-cluster-dev")
	gemini := usageItem("Gemini API", "gemini-api", "", "global", "Credit", -500.0)
	gemini.ResourceTags = map[string][]string{"nb_credit_source_resource": {"test-cluster-dev"}}
	compute := usageItem("Compute Engine", "compute-engine", "", "us-central1", "Credit", -200.0)
	compute.ResourceTags = map[string][]string{"nb_credit_source_resource": {"test-cluster-dev/some-vm"}}
	r.sync(t, []providers.UsageReportItem{gemini, compute}, true)

	rows := r.spends(t)
	require.Len(t, rows, 2)
	amounts := []float64{}
	for _, row := range rows {
		assert.True(t, row.ExcludeAggregate)
		assert.Nil(t, row.CloudResourceID)
		amounts = append(amounts, row.Amount)
	}
	assert.ElementsMatch(t, []float64{-500.0, -200.0}, amounts)
}

func TestDeriveSpends_SameResourceSameDayAggregates(t *testing.T) {
	r := newDeriveRun(t, "GCP", "test-cluster-dev")
	r.sync(t, []providers.UsageReportItem{
		usageItem("Cloud SQL", "cloud-sql", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 50.0),
		usageItem("Cloud SQL", "cloud-sql", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeTax, 5.0),
	}, true)

	rows := r.spends(t)
	require.Len(t, rows, 1)
	assert.Equal(t, 55.0, rows[0].Amount)
}

func TestDeriveSpends_AWSUniqueResourceIds(t *testing.T) {
	r := newDeriveRun(t, "AWS", "123456789012")
	r.sync(t, []providers.UsageReportItem{
		usageItem("AmazonEC2", "instance", "i-0abc123", "us-east-1", providers.UsageReportItemTypeUsage, 100.0),
		usageItem("AmazonEC2", "instance", "i-0def456", "us-east-1", providers.UsageReportItemTypeUsage, 200.0),
	}, true)

	rows := r.spends(t)
	require.Len(t, rows, 2)
	assert.ElementsMatch(t, []float64{100.0, 200.0}, []float64{rows[0].Amount, rows[1].Amount})
}

func TestDeriveSpends_DifferentRegionsSameService(t *testing.T) {
	r := newDeriveRun(t, "GCP", "test-cluster-dev")
	r.sync(t, []providers.UsageReportItem{
		usageItem("Artifact Registry", "artifact-registry", "test-cluster-dev", "asia-south1", providers.UsageReportItemTypeUsage, 50.0),
		usageItem("Artifact Registry", "artifact-registry", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 30.0),
	}, true)
	assert.Len(t, r.spends(t), 2)
}

// A previous-month run derives no resources, so lines of unknown resources get
// no cloud_resource_id; they must still stay one row per resource.
func TestDeriveSpends_UnknownResourcesStaySeparate(t *testing.T) {
	r := newDeriveRun(t, "GCP", "test-cluster-dev")
	r.sync(t, []providers.UsageReportItem{
		usageItem("Cloud Functions", "cloud-functions", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 10.0),
		usageItem("Cloud Run", "cloud-run", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 20.0),
	}, false)

	rows := r.spends(t)
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Nil(t, row.CloudResourceID)
	}
	assert.ElementsMatch(t, []float64{10.0, 20.0}, []float64{rows[0].Amount, rows[1].Amount})
}

// Rows with a resource are upserted and rows without one replaced, so a re-run
// of the same report leaves the same rows, not duplicates.
func TestDeriveSpends_RerunIsIdempotent(t *testing.T) {
	r := newDeriveRun(t, "GCP", "test-cluster-dev")
	items := []providers.UsageReportItem{
		usageItem("Artifact Registry", "artifact-registry", "test-cluster-dev", "us-central1", providers.UsageReportItemTypeUsage, 100.0),
		usageItem("Compute Engine", "compute-engine", "", "us-central1", "Credit", -200.0),
	}
	r.sync(t, items, true)
	r.sync(t, items, true)

	rows := r.spends(t)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.ExcludeAggregate {
			assert.Nil(t, row.CloudResourceID)
			assert.Equal(t, -200.0, row.Amount)
		} else {
			require.NotNil(t, row.CloudResourceID)
			assert.Equal(t, 100.0, row.Amount)
		}
	}
	var stored int
	require.NoError(t, r.dbms.QueryRowAndScan(&stored, `SELECT count(*) FROM cloud_account_usage_report WHERE account_id = $1`, r.accountId))
	assert.Equal(t, 2, stored, "the second run must replace the first run's lines")
}

// A credit or refund line that carries a resource (Azure refunds do) is stored
// without a resource, so it neither collides with that resource's usage row on
// the same day nor gets dropped.
func TestDeriveSpends_CreditWithResourceStoredWithoutResource(t *testing.T) {
	r := newDeriveRun(t, "AWS", "123456789012")
	r.sync(t, []providers.UsageReportItem{
		usageItem("AmazonEC2", "instance", "i-0abc123", "us-east-1", providers.UsageReportItemTypeUsage, 100.0),
		usageItem("AmazonEC2", "instance", "i-0abc123", "us-east-1", "Refund", -40.0),
	}, true)

	rows := r.spends(t)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.ExcludeAggregate {
			assert.Nil(t, row.CloudResourceID)
			assert.Equal(t, -40.0, row.Amount)
			assert.Equal(t, "", spendTag(t, row, "nb_resource_id"))
		} else {
			require.NotNil(t, row.CloudResourceID)
			assert.Equal(t, 100.0, row.Amount)
		}
	}
}

// A failed run leaves the previous run's lines; a successful run replaces them,
// including lines stored before runs were marked.
func TestUsageReportSink_ReplaceAndDiscard(t *testing.T) {
	r := newDeriveRun(t, "AWS", "123456789012")
	count := func() int {
		var n int
		require.NoError(t, r.dbms.QueryRowAndScan(&n, `SELECT count(*) FROM cloud_account_usage_report WHERE account_id = $1`, r.accountId))
		return n
	}
	_, err := r.dbms.Exec(`INSERT INTO cloud_account_usage_report (id, account_id, report_date, product_code, cost) VALUES (gen_random_uuid(), $1, '2026-04-01', 'legacy', 1)`, r.accountId)
	require.NoError(t, err)

	first := []providers.UsageReportItem{usageItem("AmazonEC2", "instance", "i-1", "us-east-1", providers.UsageReportItemTypeUsage, 1)}
	r.sync(t, first, true)
	assert.Equal(t, 1, count(), "legacy rows without a run marker are replaced")

	failed := newUsageReportSink(r.ctx, r.dbms, r.accountId, r.account, time.April, 2026)
	require.NoError(t, failed.write([]providers.UsageReportItem{
		usageItem("AmazonEC2", "instance", "i-2", "us-east-1", providers.UsageReportItemTypeUsage, 2),
		usageItem("AmazonEC2", "instance", "i-3", "us-east-1", providers.UsageReportItemTypeUsage, 3),
	}))
	failed.discard()
	assert.Equal(t, 1, count(), "a failed run must not touch the stored month")
}

type resourceTestRow struct {
	ResourseId string `db:"resourse_id"`
	Name       string `db:"name"`
	Region     string `db:"region"`
	Tags       string `db:"tags"`
	Status     string `db:"status"`
	IsActive   bool   `db:"is_active"`
}

func (r *deriveRun) resources(t *testing.T) map[string]resourceTestRow {
	t.Helper()
	rows := []struct {
		resourceTestRow
		ExternalResourceId *string `db:"external_resource_id"`
	}{}
	require.NoError(t, r.dbms.QueryAndScan(&rows, `SELECT resourse_id, COALESCE(name, '') AS name, region, COALESCE(tags::text, '{}') AS tags, status, is_active, external_resource_id
		FROM cloud_resourses WHERE account = $1`, r.accountId))
	out := map[string]resourceTestRow{}
	for _, row := range rows {
		key := ""
		if row.ExternalResourceId != nil {
			key = *row.ExternalResourceId
		}
		out[key] = row.resourceTestRow
	}
	return out
}

// Billing resources: first line wins, Azure names come from the line, the
// name falls back to the id, empty regions are global, and billing rows the
// run no longer bills are retired — but rows without an external id are not.
func TestDeriveBillingResources(t *testing.T) {
	r := newDeriveRun(t, "AWS", "123456789012")
	first := usageItem("AmazonEC2", "instance", "i-1", "", providers.UsageReportItemTypeUsage, 1)
	first.ResourceTags = map[string][]string{"team": {"a"}}
	second := first
	second.ResourceTags = map[string][]string{"team": {"b"}}
	gone := usageItem("AmazonEC2", "instance", "i-gone", "us-east-1", providers.UsageReportItemTypeUsage, 1)
	r.sync(t, []providers.UsageReportItem{first, second, gone}, true)
	_, err := r.dbms.Exec(`INSERT INTO cloud_resourses (id, resourse_id, type, region, service_name, account, status, is_active, meta)
		VALUES (gen_random_uuid(), 'no-erid', 'instance', 'us-east-1', 'AmazonEC2', $1, 'Active', true, '{"nb_source":"billing"}')`, r.accountId)
	require.NoError(t, err)

	r.sync(t, []providers.UsageReportItem{first, second}, true)

	got := r.resources(t)
	erid := buildExternalResourceId("AWS", "123456789012", "", "AmazonEC2", "instance", "i-1", "")
	require.Contains(t, got, erid)
	assert.Equal(t, "i-1", got[erid].Name, "name falls back to the resource id")
	assert.Equal(t, "global", got[erid].Region)
	assert.JSONEq(t, `{"team":["a"]}`, got[erid].Tags, "first line wins")
	goneErid := buildExternalResourceId("AWS", "123456789012", "us-east-1", "AmazonEC2", "instance", "i-gone", "")
	assert.False(t, got[goneErid].IsActive)
	assert.Equal(t, string(providers.ResourceStatusDeleted), got[goneErid].Status)
	assert.True(t, got[""].IsActive, "rows without an external id are never retired")

	az := newDeriveRun(t, "Azure", "sub-1")
	vm := usageItem("Virtual Machines", "virtualmachines", "/subscriptions/sub-1/resourcegroups/rg/providers/microsoft.compute/virtualmachines/vm-1", "eastus", providers.UsageReportItemTypeUsage, 3)
	vm.ResourceName = "vm-1"
	az.sync(t, []providers.UsageReportItem{vm}, true)
	for _, row := range az.resources(t) {
		assert.Equal(t, "vm-1", row.Name)
	}
}

// Credits and refunds are excluded from the notification totals, so it reports
// gross spend rather than net-of-credits (issue #29455).
func TestSummarizeDailySpend_ExcludesCreditsAndRefunds(t *testing.T) {
	r := newDeriveRun(t, "AWS", "123456789012")
	yesterday := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	earlierThisMonth := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	line := func(product string, category providers.UsageReportCostCategory, cost float64, end time.Time) providers.UsageReportItem {
		item := usageItem(product, "instance", "", "us-east-1", category, cost)
		item.CostCurrency = "USD"
		item.StartDate = end.Add(-time.Hour)
		item.EndDate = end
		return item
	}
	sink := r.sync(t, []providers.UsageReportItem{
		line("AmazonEC2", providers.UsageReportItemTypeUsage, 5.0, yesterday),
		line("AmazonEC2", providers.UsageReportItemTypeTax, 0.5, yesterday),
		line("AmazonEC2", "Credit", -100.0, yesterday),
		line("AmazonS3", "Refund", -3.0, yesterday),
		line("AmazonS3", providers.UsageReportItemTypeUsage, 7.0, earlierThisMonth),
		line("AmazonS3", "Credit", -50.0, earlierThisMonth),
	}, true)

	summary, err := summarizeDailySpend(r.dbms, r.accountId, sink, yesterday)
	require.NoError(t, err)
	assert.InDelta(t, 5.5, summary.totalDay, 1e-9)
	assert.InDelta(t, 12.5, summary.totalMonth, 1e-9)
	assert.Len(t, summary.topDailyItems, 2)
	assert.Equal(t, 5.0, summary.topDailyItems[0]["cost"])
	require.Len(t, summary.topMonthlyServices, 2)
	assert.Equal(t, "AmazonS3", summary.topMonthlyServices[0]["service"])
	assert.InDelta(t, 7.0, summary.topMonthlyServices[0]["cost"], 1e-9)
	assert.InDelta(t, 5.5, summary.topMonthlyServices[1]["cost"], 1e-9)
	assert.Equal(t, "USD", summary.currency)
	assert.False(t, summary.mixedCurrencies)
}

// randomUsageItems builds a seeded report with the cases the derivation has to
// get right: resource ids that differ only by case, several lines per resource
// with different tags, credits and taxes without a resource, empty regions and
// currencies, several days.
func randomUsageItems(provider string, n int, seed int64) []providers.UsageReportItem {
	rng := rand.New(rand.NewSource(seed))
	products := []struct{ code, resourceType string }{
		{"AmazonEC2", "instance"}, {"AWSLambda", "function"}, {"AmazonS3", "bucket"}, {"AmazonEC2", "snapshot"},
	}
	regions := []string{"us-east-1", "us-west-2", ""}
	tagSets := []map[string][]string{{}, {"team": {"a"}}, {"team": {"b"}, "env": {"prod"}}}
	items := make([]providers.UsageReportItem, 0, n)
	for i := 0; i < n; i++ {
		p := products[rng.Intn(len(products))]
		day := time.Date(2026, 4, 1+rng.Intn(5), 0, 0, 0, 0, time.UTC)
		resourceId := fmt.Sprintf("res-%d", rng.Intn(200))
		if rng.Intn(10) == 0 {
			resourceId = strings.ToUpper(resourceId)
		}
		category := providers.UsageReportItemTypeUsage
		switch roll := rng.Intn(100); {
		case roll < 8:
			category, resourceId = "Credit", ""
		case roll < 10:
			category, resourceId = "Refund", ""
		case roll < 15:
			category, resourceId = providers.UsageReportItemTypeTax, ""
		}
		if provider == "Azure" && resourceId != "" {
			resourceId = "/subscriptions/sub-1/resourcegroups/rg/providers/" + p.resourceType + "/" + resourceId
		}
		currency := "USD"
		if rng.Intn(20) == 0 {
			currency = ""
		}
		tags := map[string][]string{}
		for k, v := range tagSets[rng.Intn(len(tagSets))] {
			tags[k] = v
		}
		resourceName := ""
		if provider == "Azure" && resourceId != "" {
			resourceName = "name-" + resourceId[strings.LastIndex(resourceId, "/")+1:]
		}
		items = append(items, providers.UsageReportItem{
			ProductCode:        p.code,
			ProductServiceCode: p.code,
			ResourceType:       p.resourceType,
			ResourceId:         resourceId,
			ResourceName:       resourceName,
			ResourceArn:        ifStr(resourceId != "", "arn:"+resourceId),
			ResourceRegionCode: regions[rng.Intn(len(regions))],
			CostCategory:       category,
			Cost:               float64(rng.Intn(10000)) / 100,
			CostCurrency:       currency,
			StartDate:          day,
			EndDate:            day.Add(24 * time.Hour),
			ResourceTags:       tags,
		})
	}
	return items
}

func ifStr(cond bool, v string) string {
	if cond {
		return v
	}
	return ""
}

func cloneUsageItems(items []providers.UsageReportItem) []providers.UsageReportItem {
	out := make([]providers.UsageReportItem, len(items))
	for i, item := range items {
		tags := make(map[string][]string, len(item.ResourceTags))
		for k, v := range item.ResourceTags {
			tags[k] = v
		}
		item.ResourceTags = tags
		// Accepted drift (#38972): empty region and currency are stored as
		// global and USD, so the spend keys and tags derived from them use those.
		if item.ResourceRegionCode == "" {
			item.ResourceRegionCode = "global"
		}
		if item.CostCurrency == "" {
			item.CostCurrency = "USD"
		}
		out[i] = item
	}
	return out
}

func canonicalTags(t *testing.T, raw string) string {
	t.Helper()
	var tags map[string][]string
	require.NoError(t, json.Unmarshal([]byte(raw), &tags))
	b, err := json.Marshal(tags)
	require.NoError(t, err)
	return string(b)
}

// The SQL derivation must produce what the Go implementation it replaced
// produced, on current-month runs (resources derived first) and previous-month
// runs (no resources, lines of unknown resources keep a NULL resource).
func TestDerive_MatchesGoReference(t *testing.T) {
	for _, tc := range []struct {
		provider      string
		withResources bool
	}{{"AWS", true}, {"AWS", false}, {"GCP", true}, {"Azure", true}} {
		t.Run(fmt.Sprintf("%s/resources=%v", tc.provider, tc.withResources), func(t *testing.T) {
			r := newDeriveRun(t, tc.provider, "123456789012")
			items := randomUsageItems(tc.provider, 4000, 38972)
			r.sync(t, items, tc.withResources)
			report := providers.GetUsageReportResponse{Items: cloneUsageItems(items)}

			extIdMap := map[string]string{}
			if tc.withResources {
				want, err := referenceBillingResources(r.account, r.accountId, r.tenantId, report)
				require.NoError(t, err)
				got := r.resources(t)
				require.Greater(t, len(want), 100)
				require.Len(t, got, len(want))
				for erid, w := range want {
					g, ok := got[erid]
					require.True(t, ok, erid)
					assert.Equal(t, w["resourse_id"], g.ResourseId, erid)
					assert.Equal(t, w["name"], g.Name, erid)
					assert.Equal(t, w["region"], g.Region, erid)
					assert.JSONEq(t, w["tags"].(string), g.Tags, erid)
				}
				ids := []struct {
					Erid string `db:"external_resource_id"`
					Id   string `db:"id"`
				}{}
				require.NoError(t, r.dbms.QueryAndScan(&ids, `SELECT external_resource_id, id::text AS id FROM cloud_resourses WHERE account = $1`, r.accountId))
				for _, row := range ids {
					extIdMap[row.Erid] = row.Id
				}
			}

			wantRows, err := referenceBuildSpendMap(r.account, report, extIdMap)
			require.NoError(t, err)
			key := func(resourceId *string, date string, exclude bool, tags string) string {
				id := ""
				if resourceId != nil {
					id = *resourceId
				}
				return strings.Join([]string{id, date, fmt.Sprint(exclude), tags}, "|")
			}
			// Rows without a resource that differ only by resource share tags, so
			// compare the number of rows and their summed amount per key.
			type agg struct {
				rows   int
				amount float64
			}
			want := map[string]agg{}
			for _, row := range wantRows {
				k := key(row.CloudResourceID, row.Date, row.ExcludeAggregate, canonicalTags(t, row.Tags))
				want[k] = agg{want[k].rows + 1, want[k].amount + row.Amount}
			}
			got := map[string]agg{}
			spendRows := r.spends(t)
			for _, row := range spendRows {
				k := key(row.CloudResourceID, row.Date.Format("2006-01-02"), row.ExcludeAggregate, canonicalTags(t, row.Tags))
				got[k] = agg{got[k].rows + 1, got[k].amount + row.Amount}
				assert.Equal(t, "USD", row.Unit)
			}
			require.Len(t, spendRows, len(wantRows))
			wantKeys := make([]string, 0, len(want))
			for k := range want {
				wantKeys = append(wantKeys, k)
			}
			sort.Strings(wantKeys)
			require.Greater(t, len(wantRows), 500, "the seeded report must produce enough spend rows to compare")
			require.Len(t, got, len(want))
			for _, k := range wantKeys {
				require.Contains(t, got, k)
				assert.Equal(t, want[k].rows, got[k].rows, k)
				assert.InDelta(t, want[k].amount, got[k].amount, 1e-6, k)
			}
		})
	}
}

// A resource stored earlier under another external id (same account, id up to
// case, type, region and service) is re-keyed to the billing id rather than
// duplicated; resources already stored under their billing id are left alone.
func TestDeriveBillingResources_ReconcilesOldExternalId(t *testing.T) {
	r := newDeriveRun(t, "AWS", "123456789012")
	_, err := r.dbms.Exec(`INSERT INTO cloud_resourses (id, resourse_id, type, region, service_name, account, status, is_active, meta, external_resource_id)
		VALUES (gen_random_uuid(), 'I-OLD', 'instance', 'us-east-1', 'AmazonEC2', $1, 'Active', true, '{}', 'legacy-format-id')`, r.accountId)
	require.NoError(t, err)

	r.sync(t, []providers.UsageReportItem{
		usageItem("AmazonEC2", "instance", "i-old", "us-east-1", providers.UsageReportItemTypeUsage, 1),
		usageItem("AmazonEC2", "instance", "i-new", "us-east-1", providers.UsageReportItemTypeUsage, 2),
	}, true)

	got := r.resources(t)
	require.Len(t, got, 2, "the old row is re-keyed, not duplicated")
	oldErid := buildExternalResourceId("AWS", "123456789012", "us-east-1", "AmazonEC2", "instance", "i-old", "")
	require.Contains(t, got, oldErid)
	assert.Equal(t, "I-OLD", got[oldErid].ResourseId, "the existing row was re-keyed")
	assert.NotContains(t, got, "legacy-format-id")

	// A second run has nothing left to reconcile.
	r.sync(t, []providers.UsageReportItem{
		usageItem("AmazonEC2", "instance", "i-old", "us-east-1", providers.UsageReportItemTypeUsage, 1),
		usageItem("AmazonEC2", "instance", "i-new", "us-east-1", providers.UsageReportItemTypeUsage, 2),
	}, true)
	assert.Len(t, r.resources(t), 2)
}
