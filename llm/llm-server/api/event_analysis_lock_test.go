package api

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func TestTryEventAnalysisLock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		acquired bool
	}{
		{name: "owner", acquired: true},
		{name: "another worker owns it", acquired: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			db := sqlx.NewDb(raw, "sqlmock")
			key := "event-analysis:7:account:fingerprint"
			mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(key).
				WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(tc.acquired))
			if tc.acquired {
				mock.ExpectQuery("SELECT pg_advisory_unlock").WithArgs(key).
					WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))
			}
			mock.ExpectClose()

			unlock, acquired, err := tryEventAnalysisLock(context.Background(), db, "account", "fingerprint")
			require.NoError(t, err)
			require.Equal(t, tc.acquired, acquired)
			if acquired {
				require.NotNil(t, unlock)
				unlock()
			} else {
				require.Nil(t, unlock)
			}
			require.NoError(t, raw.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTryEventAnalysisLockFailsClosedOnDatabaseError(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs("event-analysis:7:account:fingerprint").WillReturnError(errors.New("database unavailable"))
	mock.ExpectClose()

	unlock, acquired, err := tryEventAnalysisLock(context.Background(), db, "account", "fingerprint")
	require.ErrorContains(t, err, "database unavailable")
	require.False(t, acquired)
	require.Nil(t, unlock)
	require.NoError(t, raw.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
