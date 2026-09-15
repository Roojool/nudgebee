package events

import (
	"testing"
	"time"

	"nudgebee/llm/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDuplicateChainSpan(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()
	around := time.Date(2026, 9, 13, 8, 55, 0, 0, time.UTC)

	t.Run("returns the chain's earliest and latest start near the event", func(t *testing.T) {
		repo, mock := newClaimTestRepo(t)
		first := time.Date(2026, 9, 13, 7, 38, 0, 0, time.UTC)
		last := time.Date(2026, 9, 13, 9, 10, 0, 0, time.UTC)
		mock.ExpectQuery(`select min\(e.starts_at\), max\(e.starts_at\)\s+from event_duplicates d`).
			WithArgs("acct-1", "evt-1", around.Add(-duplicateChainLookback), around.Add(duplicateChainLookahead)).
			WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(first, last))

		gotFirst, gotLast, err := repo.GetDuplicateChainSpan(ctx, "acct-1", "evt-1", around)

		require.NoError(t, err)
		require.NotNil(t, gotFirst)
		require.NotNil(t, gotLast)
		assert.True(t, first.Equal(*gotFirst))
		assert.True(t, last.Equal(*gotLast))
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no chain row returns nil span", func(t *testing.T) {
		repo, mock := newClaimTestRepo(t)
		mock.ExpectQuery(`select min\(e.starts_at\), max\(e.starts_at\)`).
			WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(nil, nil))

		gotFirst, gotLast, err := repo.GetDuplicateChainSpan(ctx, "acct-1", "evt-1", around)

		require.NoError(t, err)
		assert.Nil(t, gotFirst)
		assert.Nil(t, gotLast)
	})
}
