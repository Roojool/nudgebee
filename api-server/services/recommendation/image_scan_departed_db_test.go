package recommendation

import (
	"fmt"
	"os"
	"testing"
	"time"

	"nudgebee/services/internal/database/models"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TestArchiveDepartedImages_DB runs the departed-image sweep against a real
// Postgres planner. The statement is a destructive UPDATE driven by a three-way
// UNION over jsonb, a text extraction and a guarded timestamp cast; a mistake in
// any of those is invisible to Go and retires findings that still apply.
//
// Runs against throwaway tables, never the real ones, so it is safe against any
// database it is pointed at. DB-gated: skips with no database, fails in the job
// that provisions one.
func TestArchiveDepartedImages_DB(t *testing.T) {
	// Opens its own connection rather than the shared manager. Fetching the
	// shared one caches it for the package, after which a sibling test that
	// registers a mock manager silently keeps getting this real one instead.
	url := os.Getenv("APP_DATABASE_URL")
	if url == "" {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatal("REQUIRE_DB_TESTS is set but APP_DATABASE_URL is empty")
		}
		t.Skip("skipping: APP_DATABASE_URL is not set")
	}
	dbms, err := sqlx.Connect("postgres", url)
	if err != nil {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatalf("REQUIRE_DB_TESTS is set but the database is not accessible: %v", err)
		}
		t.Skipf("skipping: database not accessible: %v", err)
	}
	t.Cleanup(func() { _ = dbms.Close() })

	const (
		recTable  = "zz_departed_rec_test"
		podTable  = "zz_departed_pod_test"
		wlTable   = "zz_departed_workload_test"
		account   = "acc-1"
		tenant    = "tenant-1"
		staleAcct = "acc-stale"
	)

	mustExec := func(q string, args ...any) {
		_, e := dbms.Exec(q, args...)
		require.NoError(t, e)
	}
	drop := func() {
		for _, tbl := range []string{recTable, podTable, wlTable} {
			_, _ = dbms.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl))
		}
	}
	drop()
	t.Cleanup(drop)

	mustExec(fmt.Sprintf(`CREATE TABLE %s (
		id text PRIMARY KEY, cloud_account_id text, tenant_id text, category text,
		rule_name text, status text, account_object_id text, recommendation jsonb)`, recTable))
	mustExec(fmt.Sprintf(`CREATE TABLE %s (
		cloud_account_id text, tenant_id text, last_seen timestamptz, meta jsonb)`, podTable))
	mustExec(fmt.Sprintf(`CREATE TABLE %s (
		cloud_account_id text, tenant_id text, is_active boolean, meta jsonb)`, wlTable))

	containers := func(images ...string) string {
		out := `{"config":{"containers":[`
		for i, img := range images {
			if i > 0 {
				out += ","
			}
			out += fmt.Sprintf(`{"image":%q}`, img)
		}
		return out + `]}}`
	}

	// Inventory. The account is fresh; a pod reported minutes ago proves the
	// agent is reporting, which is what makes any archiving eligible at all.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,now(),$3::jsonb)`, podTable),
		account, tenant, containers("live:1"))
	// A pod row that outlived its pod still counts as a sighting, which is what
	// keeps a CronJob between runs from being treated as gone.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,now() - interval '3 days',$3::jsonb)`, podTable),
		account, tenant, containers("cronjob:1"))
	// A workload scaled to zero declares an image but has no pods.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,true,$3::jsonb)`, wlTable),
		account, tenant, containers("scaled-to-zero:1"))
	// An account whose agent stopped reporting must be skipped entirely.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,now() - interval '30 days',$3::jsonb)`, podTable),
		staleAcct, tenant, containers("whatever:1"))

	// A JSON null container list is not a SQL NULL, so coalesce lets it through
	// and expanding it aborts the statement. One such row anywhere would stop the
	// sweep for every account, so it has to be skipped rather than expanded.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,now(),'{"config":{"containers":null}}'::jsonb)`, podTable),
		account, tenant)
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,now(),'{"config":{}}'::jsonb)`, podTable),
		account, tenant)
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,true,'{"config":{"containers":{"image":"not-a-list"}}}'::jsonb)`, wlTable),
		account, tenant)

	// The scanner confirming an image recently counts as a sighting even when the
	// inventory does not model it, such as an init-container-only image.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES
		('sum-1',$1,$2,'Security','image_scan_summary','Open','init-only:1',$3::jsonb)`, recTable),
		account, tenant, fmt.Sprintf(`{"scanned_at":%q}`, time.Now().UTC().Format(time.RFC3339)))
	// A summary whose scan is older than the window is not a current sighting.
	mustExec(fmt.Sprintf(`INSERT INTO %s VALUES
		('sum-2',$1,$2,'Security','image_scan_summary','Open','long-ago:1',$3::jsonb)`, recTable),
		account, tenant, fmt.Sprintf(`{"scanned_at":%q}`,
			time.Now().UTC().AddDate(0, 0, -(imageScanFreshDays+30)).Format(time.RFC3339)))

	finding := func(id, image, status, acct string) {
		mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,$3,'Security','image_scan',$4,$5,$6::jsonb)`, recTable),
			id, acct, tenant, status, image+"-CVE-1", fmt.Sprintf(`{"image_name":%q}`, image))
	}
	finding("keep-live", "live:1", "Open", account)
	finding("keep-cronjob", "cronjob:1", "Open", account)
	finding("keep-scaled", "scaled-to-zero:1", "Open", account)
	finding("keep-init", "init-only:1", "Open", account)
	finding("keep-stale-account", "gone:1", "Open", staleAcct)
	finding("archive-gone", "gone:1", "Open", account)
	finding("archive-long-ago", "long-ago:1", "Open", account)
	// User-owned states are set by a person and must survive.
	finding("keep-dismissed", "gone:1", "Dismissed", account)
	finding("keep-inprogress", "gone:1", "InProgress", account)
	finding("keep-closed", "gone:1", "Closed", account)

	_, err = dbms.Exec(departedImageArchiveQuery(recTable, podTable, wlTable),
		imageScanInventoryFreshHours, imageScanFreshDays, imageScanDepartedBatchLimit,
		models.RecommendationStatusArchive, models.RecommendationStatusOpen)
	require.NoError(t, err)

	want := map[string]string{
		"keep-live":          "Open",
		"keep-cronjob":       "Open",
		"keep-scaled":        "Open",
		"keep-init":          "Open",
		"keep-stale-account": "Open",
		"archive-gone":       "Archive",
		"archive-long-ago":   "Archive",
		"keep-dismissed":     "Dismissed",
		"keep-inprogress":    "InProgress",
		"keep-closed":        "Closed",
	}
	for id, expected := range want {
		var got string
		require.NoError(t, dbms.Get(&got,
			fmt.Sprintf(`SELECT status FROM %s WHERE id = $1`, recTable), id))
		if got != expected {
			t.Errorf("%s: status is %q, expected %q", id, got, expected)
		}
	}
}
