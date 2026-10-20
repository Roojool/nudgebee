// Package pgbatch splits multi-row named statements into batches that stay
// under Postgres's per-statement bind-parameter limit.
package pgbatch

import (
	"fmt"

	"github.com/jmoiron/sqlx"
)

// MaxBindParams is the most bind parameters Postgres accepts in one statement
// (the wire protocol encodes the count as a uint16).
const MaxBindParams = 65535

// ForEach calls fn with consecutive slices of rows, each small enough that
// expanding query's VALUES clause over it stays within MaxBindParams. The
// per-row parameter count is derived from query itself, so it cannot drift
// from the column list. Errors from fn are returned unwrapped so callers can
// still type-assert *pq.Error. Empty rows is a no-op.
func ForEach[T any](query string, rows []T, fn func(batch []T) error) error {
	if len(rows) == 0 {
		return nil
	}
	_, args, err := sqlx.Named(query, rows[:1])
	if err != nil {
		return fmt.Errorf("pgbatch: bind first row: %w", err)
	}
	size := len(rows)
	if perRow := len(args); perRow > 0 {
		size = max(1, MaxBindParams/perRow)
	}
	for start := 0; start < len(rows); start += size {
		if err := fn(rows[start:min(start+size, len(rows))]); err != nil {
			return err
		}
	}
	return nil
}

// NamedExec runs query via e.NamedExec once per batch of rows (see ForEach).
func NamedExec[T any](e sqlx.Ext, query string, rows []T) error {
	return ForEach(query, rows, func(batch []T) error {
		_, err := sqlx.NamedExec(e, query, batch)
		return err
	})
}
