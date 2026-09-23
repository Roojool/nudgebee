package api

import (
	"context"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
)

// tryEventAnalysisLock serializes the whole event pipeline across HTTP, MQ and
// recovery workers. The conversation session is shared by account and
// fingerprint, even when events have different aggregation keys.
func tryEventAnalysisLock(ctx context.Context, db *sqlx.DB, accountID, fingerprint string) (func(), bool, error) {
	conn, err := db.Connx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("event analysis lock connection: %w", err)
	}
	key := fmt.Sprintf("event-analysis:%d:%s:%s", len(accountID), accountID, fingerprint)
	var acquired bool
	err = conn.QueryRowxContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&acquired)
	if err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("event analysis lock acquisition: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		unlockErr := conn.QueryRowxContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key).Scan(&released)
		if unlockErr != nil || !released {
			slog.Error("analyzer: unable to release event analysis lock", "error", unlockErr, "account_id", accountID, "fingerprint", fingerprint)
			// A session lock must never return to the pool still held.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, true, nil
}
