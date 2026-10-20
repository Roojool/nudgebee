package pgbatch

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const threeColumnInsert = `INSERT INTO t (a, b, c) VALUES (:a, :b, :c)
	ON CONFLICT (a) DO UPDATE SET b = EXCLUDED.b, c = EXCLUDED.c`

type row struct {
	A int    `db:"a"`
	B string `db:"b"`
	C bool   `db:"c"`
}

func batchSizes[T any](t *testing.T, query string, rows []T) []int {
	t.Helper()
	var sizes []int
	require.NoError(t, ForEach(query, rows, func(batch []T) error {
		sizes = append(sizes, len(batch))
		return nil
	}))
	return sizes
}

func TestForEachDerivesBatchSizeFromQuery(t *testing.T) {
	perBatch := MaxBindParams / 3
	rows := make([]row, perBatch*2+5)

	assert.Equal(t, []int{perBatch, perBatch, 5}, batchSizes(t, threeColumnInsert, rows))
}

func TestForEachSupportsMapRows(t *testing.T) {
	rows := make([]map[string]any, MaxBindParams/3+1)
	for i := range rows {
		rows[i] = map[string]any{"a": i, "b": "x", "c": true}
	}

	assert.Equal(t, []int{MaxBindParams / 3, 1}, batchSizes(t, threeColumnInsert, rows))
}

func TestForEachUnderLimitIsOneBatch(t *testing.T) {
	assert.Equal(t, []int{10}, batchSizes(t, threeColumnInsert, make([]row, 10)))
}

func TestForEachEmptyRowsIsNoop(t *testing.T) {
	assert.Nil(t, batchSizes(t, threeColumnInsert, []row{}))
}

func TestForEachStopsAndReturnsCallbackErrorUnwrapped(t *testing.T) {
	sentinel := errors.New("boom")
	calls := 0
	err := ForEach(threeColumnInsert, make([]row, MaxBindParams), func([]row) error {
		calls++
		return sentinel
	})

	assert.Same(t, sentinel, err)
	assert.Equal(t, 1, calls)
}

func TestForEachRejectsRowMissingABoundName(t *testing.T) {
	err := ForEach(threeColumnInsert, []map[string]any{{"a": 1}}, func([]map[string]any) error {
		t.Fatal("callback must not run when the first row cannot be bound")
		return nil
	})

	assert.Error(t, err)
}

// TestNamedExecKeepsEveryStatementUnderTheLimit runs a row count that a single
// statement could not carry and checks each issued statement's argument count.
func TestNamedExecKeepsEveryStatementUnderTheLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	rows := make([]row, MaxBindParams/3+7)
	for i := range rows {
		rows[i] = row{A: i, B: fmt.Sprint(i), C: i%2 == 0}
	}
	for _, n := range []int{MaxBindParams / 3, 7} {
		args := make([]driver.Value, 0, n*3)
		for range n * 3 {
			args = append(args, sqlmock.AnyArg())
		}
		mock.ExpectExec(`INSERT INTO t`).WithArgs(args...).
			WillReturnResult(sqlmock.NewResult(0, int64(n)))
	}

	require.NoError(t, NamedExec(sqlx.NewDb(db, "postgres"), threeColumnInsert, rows))
	assert.NoError(t, mock.ExpectationsWereMet())
}
