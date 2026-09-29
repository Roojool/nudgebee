package budget

import (
	"errors"
	"testing"
	"time"

	"nudgebee/llm/common"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each test uses its own entity id: the cache namespace is process-wide.

func TestCachedEntityTokenUsage_ReusesTotalWithinTTL(t *testing.T) {
	dm, mock := newMockMetastoreDM(t)
	mock.ExpectQuery("WITH per_call_cost").
		WithArgs("acct-cache-hit").
		WillReturnRows(sqlmock.NewRows([]string{"total_cost"}).AddRow(12.5))

	first, err := cachedEntityTokenUsage(dm, EntityTypeAccount, "acct-cache-hit", ModuleUserInvestigation, "month")
	require.NoError(t, err)
	second, err := cachedEntityTokenUsage(dm, EntityTypeAccount, "acct-cache-hit", ModuleUserInvestigation, "month")
	require.NoError(t, err)

	assert.Equal(t, 12.5, first)
	assert.Equal(t, 12.5, second)
	assert.NoError(t, mock.ExpectationsWereMet(), "the second check must not query again")
}

func TestCachedEntityTokenUsage_DoesNotCacheErrors(t *testing.T) {
	dm, mock := newMockMetastoreDM(t)
	mock.ExpectQuery("WITH per_call_cost").
		WithArgs("acct-cache-err").
		WillReturnError(errors.New("connection reset"))
	mock.ExpectQuery("WITH per_call_cost").
		WithArgs("acct-cache-err").
		WillReturnRows(sqlmock.NewRows([]string{"total_cost"}).AddRow(3.0))

	_, err := cachedEntityTokenUsage(dm, EntityTypeAccount, "acct-cache-err", ModuleInvestigation, "month")
	require.Error(t, err)
	cost, err := cachedEntityTokenUsage(dm, EntityTypeAccount, "acct-cache-err", ModuleInvestigation, "month")
	require.NoError(t, err)

	assert.Equal(t, 3.0, cost)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCachedEntityTokenUsage_RefreshesExpiredTotal(t *testing.T) {
	dm, mock := newMockMetastoreDM(t)
	stale, err := common.MarshalJson(cachedUsage{Cost: 1.0, ExpiresAt: time.Now().Add(-time.Second)})
	require.NoError(t, err)
	key := enforcedUsageKey(EntityTypeTenant, "tenant-cache-exp", ModuleInvestigation, "day", time.Now())
	require.NoError(t, common.CacheSet(enforcedUsageCacheNS, key, stale))
	mock.ExpectQuery("WITH per_call_cost").
		WithArgs("tenant-cache-exp").
		WillReturnRows(sqlmock.NewRows([]string{"total_cost"}).AddRow(9.0))

	cost, err := cachedEntityTokenUsage(dm, EntityTypeTenant, "tenant-cache-exp", ModuleInvestigation, "day")
	require.NoError(t, err)

	assert.Equal(t, 9.0, cost)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestEnforcedUsageKey_ChangesWhenThePeriodRollsOver(t *testing.T) {
	lastSecond := time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)
	nextSecond := lastSecond.Add(time.Second)

	assert.NotEqual(t,
		enforcedUsageKey(EntityTypeAccount, "a", ModuleInvestigation, "month", lastSecond),
		enforcedUsageKey(EntityTypeAccount, "a", ModuleInvestigation, "month", nextSecond),
		"a September total must not be reused on October 1")
	assert.NotEqual(t,
		enforcedUsageKey(EntityTypeAccount, "a", ModuleInvestigation, "day", lastSecond),
		enforcedUsageKey(EntityTypeAccount, "a", ModuleInvestigation, "day", nextSecond))
	assert.Equal(t,
		enforcedUsageKey(EntityTypeAccount, "a", ModuleInvestigation, "month", lastSecond),
		enforcedUsageKey(EntityTypeAccount, "a", ModuleInvestigation, "month", lastSecond.Add(-time.Hour)),
		"within a period the key is stable, or the cache would never hit")
}
