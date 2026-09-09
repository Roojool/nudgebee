package event

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// The enrichment merge is a single hand-written statement against a table with
// NOT NULL columns and a jsonb array. Every defect it has had so far — a uuid
// column compared against text, a NULL array wiping the column, and a NOT NULL
// violation when an event arrived with no cluster — was invisible to Go tests
// and only appeared against a real Postgres. Hence this suite.
//
// Named _DB so the "PR lifecycle sweeps (real Postgres)" job's -run '_DB$'
// filter picks it up. Skipped locally unless a database is configured.
func enrichmentTestDB(t *testing.T) *sqlx.DB {
	t.Helper()

	dsn := os.Getenv("APP_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_POSTGRES_DSN")
	}
	if dsn == "" {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatal("REQUIRE_DB_TESTS is set but neither APP_DATABASE_URL nor TEST_POSTGRES_DSN is: " +
				"this suite must not be skipped in the job that exists to run it")
		}
		t.Skip("no database configured; export APP_DATABASE_URL to run this suite")
	}

	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	// One connection, so the search_path set below holds for every later statement.
	db.SetMaxOpenConns(1)

	// Unique per test, not just per process: these run against whatever database
	// APP_DATABASE_URL points at, which locally is the shared dev instance. A
	// name derived only from the pid means a second run — a colleague's, or a
	// parallel job — drops the schema this one is using mid-test.
	schema := fmt.Sprintf("enrichment_merge_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	_, err = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE; CREATE SCHEMA %s; SET search_path TO %s`,
		schema, schema, schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
		_ = db.Close()
	})

	// Mirrors the real events table for the columns this statement touches. The
	// NOT NULL on cluster and subject_name is the point of the fixture, not an
	// incidental detail — dropping it would make this suite pass on a bug.
	_, err = db.Exec(`CREATE TABLE events (
		id                uuid PRIMARY KEY,
		evidences         jsonb NOT NULL DEFAULT '[]'::jsonb,
		labels            jsonb,
		cloud_resource_id uuid,
		cluster           text NOT NULL,
		subject_namespace text,
		subject_name      text NOT NULL,
		status            text NOT NULL DEFAULT 'FIRING',
		ends_at           timestamp,
		updated_at        timestamp
	)`)
	require.NoError(t, err)

	return db
}

type mergeRow struct {
	Evidences        string  `db:"evidences"`
	Labels           *string `db:"labels"`
	Cluster          string  `db:"cluster"`
	SubjectNamespace *string `db:"subject_namespace"`
	SubjectName      string  `db:"subject_name"`
	Status           string  `db:"status"`
}

func seedEvent(t *testing.T, db *sqlx.DB, id, evidences, cluster, subjectName string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO events (id, evidences, labels, cluster, subject_name) VALUES ($1, $2::jsonb, '{}'::jsonb, $3, $4)`,
		id, evidences, cluster, subjectName)
	require.NoError(t, err)
}

func runMerge(t *testing.T, db *sqlx.DB, id string, regenerated []string, added []any, labels map[string]string,
	cloudResourceID, cluster, namespace, subjectName string) error {
	t.Helper()
	addedJSON, err := json.Marshal(added)
	require.NoError(t, err)
	labelsJSON, err := json.Marshal(labels)
	require.NoError(t, err)

	_, execErr := db.Exec(enrichmentMergeSQL,
		pq.Array(regenerated), string(addedJSON), string(labelsJSON),
		cloudResourceID, cluster, namespace, subjectName, time.Now().UTC(), id)
	return execErr
}

func fetchRow(t *testing.T, db *sqlx.DB, id string) mergeRow {
	t.Helper()
	var row mergeRow
	require.NoError(t, db.Get(&row, `SELECT evidences, labels, cluster, subject_namespace, subject_name, status FROM events WHERE id=$1`, id))
	return row
}

func cardCount(t *testing.T, raw string) int {
	t.Helper()
	var cards []any
	require.NoError(t, json.Unmarshal([]byte(raw), &cards))
	return len(cards)
}

// The regression. An event with no cluster and no subject — the common case, not
// an exotic one — made both COALESCE arms NULL, and both columns are NOT NULL, so
// the entire update failed and the event ended up with no evidence at all. On dev
// this took out roughly three quarters of all enrichments.
func TestEnrichmentMerge_EmptyClusterAndSubjectDoNotViolateNotNull_DB(t *testing.T) {
	db := enrichmentTestDB(t)
	id := "11111111-1111-1111-1111-111111111111"
	seedEvent(t, db, id, `[{"additional_info":{"action_name":"Raw Event"}}]`, "", "")

	err := runMerge(t, db, id,
		[]string{"cloud_logs"},
		[]any{map[string]any{"additional_info": map[string]any{"action_name": "cloud_logs"}}},
		map[string]string{"k": "v"},
		"", "", "", "")

	require.NoError(t, err, "an event with no cluster or subject must still be enrichable")

	row := fetchRow(t, db, id)
	require.Equal(t, "", row.Cluster)
	require.Equal(t, "", row.SubjectName)
	require.Equal(t, 2, cardCount(t, row.Evidences), "inline card kept, enrichment card appended")
}

func TestEnrichmentMerge_ResolvedValuesFillEmptyColumns_DB(t *testing.T) {
	db := enrichmentTestDB(t)
	id := "22222222-2222-2222-2222-222222222222"
	seedEvent(t, db, id, `[]`, "", "")

	require.NoError(t, runMerge(t, db, id, nil, []any{}, nil, "", "prod", "payments", "api-7"))

	row := fetchRow(t, db, id)
	require.Equal(t, "prod", row.Cluster)
	require.Equal(t, "api-7", row.SubjectName)
	require.NotNil(t, row.SubjectNamespace)
	require.Equal(t, "payments", *row.SubjectNamespace)
}

func TestEnrichmentMerge_DoesNotClobberExistingValues_DB(t *testing.T) {
	db := enrichmentTestDB(t)
	id := "33333333-3333-3333-3333-333333333333"
	seedEvent(t, db, id, `[]`, "existing-cluster", "existing-subject")

	require.NoError(t, runMerge(t, db, id, nil, []any{}, nil, "", "new-cluster", "new-ns", "new-subject"))

	row := fetchRow(t, db, id)
	require.Equal(t, "existing-cluster", row.Cluster, "a value already resolved must win")
	require.Equal(t, "existing-subject", row.SubjectName)
}

// A card with no additional_info must survive. `<> ALL(...)` against a NULL left
// side yields NULL, and WHERE drops the row — so without the coalesce on each
// side the statement silently deletes every unnamed card.
func TestEnrichmentMerge_UnnamedCardsSurvive_DB(t *testing.T) {
	db := enrichmentTestDB(t)
	id := "44444444-4444-4444-4444-444444444444"
	seedEvent(t, db, id,
		`[{"data":"no additional_info"},{"additional_info":{}},{"additional_info":{"action_name":"Raw Event"}}]`,
		"c", "s")

	require.NoError(t, runMerge(t, db, id, []string{"cloud_logs"}, []any{}, nil, "", "", "", ""))

	require.Equal(t, 3, cardCount(t, fetchRow(t, db, id).Evidences),
		"cards lacking an action_name must not be filtered out")
}

// A NULL regenerated array would make every row's predicate NULL and wipe the
// column. Unreachable from Go today, guarded in SQL because it is silent.
func TestEnrichmentMerge_NullRegeneratedArrayKeepsCards_DB(t *testing.T) {
	db := enrichmentTestDB(t)
	id := "55555555-5555-5555-5555-555555555555"
	seedEvent(t, db, id, `[{"additional_info":{"action_name":"Raw Event"}}]`, "c", "s")

	_, err := db.Exec(enrichmentMergeSQL,
		nil, `[]`, `{}`, "", "", "", "", time.Now().UTC(), id)
	require.NoError(t, err)

	require.Equal(t, 1, cardCount(t, fetchRow(t, db, id).Evidences))
}

// Redelivery must converge, not stack duplicates, and must never resurrect a
// resolved event.
func TestEnrichmentMerge_IsIdempotentAndLeavesStatusAlone_DB(t *testing.T) {
	db := enrichmentTestDB(t)
	id := "66666666-6666-6666-6666-666666666666"
	seedEvent(t, db, id, `[{"additional_info":{"action_name":"Raw Event"}}]`, "c", "s")
	_, err := db.Exec(`UPDATE events SET status='RESOLVED' WHERE id=$1`, id)
	require.NoError(t, err)

	added := []any{map[string]any{"additional_info": map[string]any{"action_name": "cloud_logs"}}}
	for i := 0; i < 3; i++ {
		require.NoError(t, runMerge(t, db, id, []string{"cloud_logs"}, added, nil, "", "", "", ""))
	}

	row := fetchRow(t, db, id)
	require.Equal(t, 2, cardCount(t, row.Evidences), "three runs must not stack three copies")
	require.Equal(t, "RESOLVED", row.Status, "enrichment must never touch status")
}
