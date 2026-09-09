package observability

import (
	"fmt"
	"os"
	"testing"

	"nudgebee/services/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkloadNameMatch_DB executes the workload-resolution predicate against a
// real Postgres planner. It is the only way to assert what the fix actually
// changed: `LIKE name || '-%'` versus `= name OR LIKE name || '-%'` is a
// difference in SQL semantics, not in Go, and the wrong one fails silently —
// the card simply renders without its container split, its request/limit lines
// or its insights, with no error anywhere.
//
// Runs against a throwaway table, never k8s_workloads, so it is safe against any
// database it is pointed at. DB-gated: skips with no database, fails in the job
// that provisions one.
func TestWorkloadNameMatch_DB(t *testing.T) {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatalf("REQUIRE_DB_TESTS is set but the database is not accessible: %v", err)
		}
		t.Skipf("skipping: database not accessible: %v", err)
	}

	const table = "zz_workload_name_match_test"
	mustExec := func(q string, args ...any) {
		_, e := dbms.Db.Exec(q, args...)
		require.NoError(t, e)
	}
	mustExec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, table))
	mustExec(fmt.Sprintf(`CREATE TABLE %s (
		cloud_account_id text, namespace text, name text, is_active boolean)`, table))
	t.Cleanup(func() { _, _ = dbms.Db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, table)) })

	const account, namespace = "acc-1", "ns-1"
	for _, w := range []struct {
		name   string
		active bool
	}{
		{"report-worker", true},
		{"foo", true},     // prefix of foo-bar; must lose the tie-break
		{"foo-bar", true}, // exact match for pod_name "foo-bar"
		{"retired", false},
	} {
		mustExec(fmt.Sprintf(`INSERT INTO %s VALUES ($1,$2,$3,$4)`, table),
			account, namespace, w.name, w.active)
	}

	// The production predicate verbatim, against the throwaway table.
	query := fmt.Sprintf(`SELECT name FROM %s
		WHERE cloud_account_id = $1 AND namespace = $2 AND %s AND is_active IS NOT FALSE
		ORDER BY length(name) DESC LIMIT 1`, table, workloadNameMatch)

	resolve := func(podName string) string {
		var name string
		if err := dbms.Db.QueryRowx(query, account, namespace, podName).Scan(&name); err != nil {
			return "" // no row
		}
		return name
	}

	// The agent case this fix exists for: pod_name IS the workload name.
	assert.Equal(t, "report-worker", resolve("report-worker"),
		"a workload name must resolve; prefix-only matching returned nothing here")

	// The alert case, unchanged: pod_name is a real pod name.
	assert.Equal(t, "report-worker", resolve("report-worker-76c589bdc5-vth5x"))

	// Ambiguity: "foo-bar" matches "foo" by prefix and "foo-bar" exactly. The
	// longest name wins, so the exact match does — the added arm cannot make a
	// shorter workload outrank a longer one.
	assert.Equal(t, "foo-bar", resolve("foo-bar"))
	assert.Equal(t, "foo-bar", resolve("foo-bar-7c78f68568-ps2bb"))
	assert.Equal(t, "foo", resolve("foo-1"), "a genuine one-segment pod of foo still resolves to foo")

	// Guards unaffected by the change.
	assert.Equal(t, "", resolve("retired"), "inactive workloads stay excluded")
	assert.Equal(t, "", resolve("report-worker2"), "a non-hyphen extension is not a prefix match")
	assert.Equal(t, "", resolve("unknown-abc"))
}
