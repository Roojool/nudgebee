package account

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"testing"

	"nudgebee/services/internal/database"
)

type plannedDelete struct {
	table    string
	column   string
	where    string
	optional bool
}

func flattenDeletePlan(plans ...[]deleteGroup) []plannedDelete {
	var steps []plannedDelete
	for _, plan := range plans {
		for _, group := range plan {
			for _, table := range group.tables {
				steps = append(steps, plannedDelete{
					table:    table.name,
					column:   table.accountColumn(group.defaultAccountIdColumn),
					where:    table.whereSubclause,
					optional: table.optional,
				})
			}
		}
	}
	return steps
}

// deletesOnColumn reports whether some step clears table by comparing column
// with the account id, either directly or inside its where-subclause.
func deletesOnColumn(steps []plannedDelete, table, column string) bool {
	onColumn := regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + `\s*=\s*\$1\b`)
	for _, step := range steps {
		if step.table != table {
			continue
		}
		if step.where == "" && step.column == column {
			return true
		}
		if step.where != "" && onColumn.MatchString(step.where) {
			return true
		}
	}
	return false
}

// TestAccountDeletePlanCoversForeignKeys_DB checks DeleteAccount's table plan
// against the foreign keys of a real schema. Account deletion broke three times
// the same way: a migration added an account-scoped table with a RESTRICT or
// NO ACTION foreign key, the plan was not updated, and every account with a row
// in that table became impossible to delete.
//
// migrations-validate.yaml runs it against a freshly migrated database, so it
// sees what the migrations create. Objects that predate the migration history
// (older dev/test/prod databases) are only covered when APP_DATABASE_URL points
// at such a database; the test only reads the catalog.
//
//	cd api-server/services && APP_DATABASE_URL='postgres://postgres:postgres@localhost:5432/appdb?sslmode=disable' \
//	  REQUIRE_DB_TESTS=true go test ./account/ -run 'TestAccountDeletePlanCoversForeignKeys_DB' -v
func TestAccountDeletePlanCoversForeignKeys_DB(t *testing.T) {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		if os.Getenv("REQUIRE_DB_TESTS") == "true" {
			t.Fatalf("REQUIRE_DB_TESTS is set but the database is not accessible: %v", err)
		}
		t.Skipf("skipping: database not accessible: %v", err)
	}
	db := dbms.Db

	tableExists := func(table string) bool {
		var exists bool
		if err := db.Get(&exists, "SELECT to_regclass($1) IS NOT NULL", "public."+table); err != nil {
			t.Fatalf("checking table %s: %v", table, err)
		}
		return exists
	}
	columnExists := func(table, column string) bool {
		var exists bool
		err := db.Get(&exists, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`, table, column)
		if err != nil {
			t.Fatalf("checking column %s.%s: %v", table, column, err)
		}
		return exists
	}

	if !tableExists("cloud_accounts") {
		t.Fatal("cloud_accounts does not exist; point APP_DATABASE_URL at a migrated database")
	}

	steps := flattenDeletePlan(accountDataDeletePlan, accountRowDeletePlan)
	// A table listed more than once starts losing rows at its first step
	// (position) and has finished at its last (lastPosition); ordering is
	// checked against both ends so either reading is safe.
	position := map[string]int{}
	lastPosition := map[string]int{}
	for i, step := range steps {
		if _, ok := position[step.table]; !ok {
			position[step.table] = i
		}
		lastPosition[step.table] = i
	}
	if _, ok := position["cloud_accounts"]; !ok {
		t.Fatal("the delete plan never deletes cloud_accounts")
	}

	var problems []string

	// A misspelt or dropped table or column would fail every delete.
	for _, step := range steps {
		if !tableExists(step.table) {
			if !step.optional {
				problems = append(problems, fmt.Sprintf("the plan clears %s, which does not exist", step.table))
			}
			continue
		}
		if step.where == "" && !columnExists(step.table, step.column) {
			problems = append(problems, fmt.Sprintf("the plan clears %s on %s, which does not exist", step.table, step.column))
		}
	}
	for _, blocker := range accountDeleteBlockers {
		if !tableExists(blocker.table) || !columnExists(blocker.table, blocker.accountColumnName) {
			problems = append(problems, fmt.Sprintf("the delete precondition counts %s.%s, which does not exist", blocker.table, blocker.accountColumnName))
		}
	}

	type foreignKey struct {
		Child    string `db:"child"`
		Parent   string `db:"parent"`
		OnDelete string `db:"on_delete"`
		Column   string `db:"child_column"`
	}
	var foreignKeys []foreignKey
	// conparentid = 0 skips the per-partition copies of a partitioned table's
	// constraint. Only the first column is read: every key that matters here
	// points at a single-column id.
	err = db.Select(&foreignKeys, `
		SELECT child.relname AS child, parent.relname AS parent, c.confdeltype::text AS on_delete,
		       (SELECT a.attname FROM pg_attribute a WHERE a.attrelid = c.conrelid AND a.attnum = c.conkey[1]) AS child_column
		FROM pg_constraint c
		JOIN pg_class child ON child.oid = c.conrelid
		JOIN pg_class parent ON parent.oid = c.confrelid
		WHERE c.contype = 'f' AND c.conparentid = 0
		  AND child.relnamespace = 'public'::regnamespace
		  AND parent.relnamespace = 'public'::regnamespace`)
	if err != nil {
		t.Fatalf("reading foreign keys: %v", err)
	}

	// cleared adds the tables emptied by ON DELETE CASCADE, at the step of the
	// table whose delete cascades into them.
	cleared := maps.Clone(position)
	for changed := true; changed; {
		changed = false
		for _, fk := range foreignKeys {
			parentStep, ok := cleared[fk.Parent]
			if fk.OnDelete != "c" || !ok {
				continue
			}
			if childStep, ok := cleared[fk.Child]; !ok || parentStep < childStep {
				cleared[fk.Child] = parentStep
				changed = true
			}
		}
	}

	isBlocker := func(table, column string) bool {
		return slices.ContainsFunc(accountDeleteBlockers, func(b accountDeleteBlocker) bool {
			return b.table == table && b.accountColumnName == column
		})
	}

	for _, fk := range foreignKeys {
		// a = NO ACTION, r = RESTRICT: both fail the delete while a row still
		// points at a deleted one.
		if (fk.OnDelete != "a" && fk.OnDelete != "r") || fk.Child == fk.Parent {
			continue
		}
		parentStep, ok := cleared[fk.Parent]
		if !ok {
			continue
		}
		if fk.Parent == "cloud_accounts" && isBlocker(fk.Child, fk.Column) {
			continue
		}
		childStep, ok := lastPosition[fk.Child]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s.%s references %s, which DeleteAccount clears, but the plan never clears %s", fk.Child, fk.Column, fk.Parent, fk.Child))
			continue
		case childStep > parentStep:
			problems = append(problems, fmt.Sprintf("%s is cleared after %s, which it references through %s", fk.Child, fk.Parent, fk.Column))
		}
		if fk.Parent == "cloud_accounts" && !deletesOnColumn(steps, fk.Child, fk.Column) {
			problems = append(problems, fmt.Sprintf("%s is never cleared on %s, its foreign key to cloud_accounts, so rows tied to the account only through it survive", fk.Child, fk.Column))
		}
	}

	slices.Sort(problems)
	for _, problem := range slices.Compact(problems) {
		t.Error(problem)
	}
}
