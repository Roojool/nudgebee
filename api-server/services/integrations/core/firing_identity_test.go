package core

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"nudgebee/services/internal/database"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sameWallClock matches a time.Time argument on its wall clock and offset, not
// just the instant. starts_at is `timestamp without time zone` and lib/pq drops
// the offset on write, so the lookup range only lines up with the stored value
// if it carries the same offset the insert did.
type sameWallClock struct{ want time.Time }

func (m sameWallClock) Match(v driver.Value) bool {
	got, ok := v.(time.Time)
	if !ok {
		return false
	}
	_, gotOffset := got.Zone()
	_, wantOffset := m.want.Zone()
	return got.Equal(m.want) && gotOffset == wantOffset
}

func firingDelivery(startsAt time.Time) EventIncomingWebhook {
	return EventIncomingWebhook{
		EventId:        "fp1",
		FiringKey:      FiringKeyFromStart("fp1", startsAt),
		EventCreatedAt: startsAt,
		Investigation:  EventIncomingWebhookInvestigation{Fingerprint: "fp1"},
	}
}

// mockDB is a private sqlmock-backed manager. firingFindingIDFor takes the manager
// as a parameter, so these tests leave the package's shared Metastore alone.
func mockDB(t *testing.T) (*database.DatabaseManager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, mock
}

func TestFiringKeyFromStart(t *testing.T) {
	start := time.Date(2026, 6, 1, 10, 0, 0, 250_000_000, time.UTC)
	assert.Equal(t, "1780308000", FiringKeyFromStart("fp1", start))
	assert.Empty(t, FiringKeyFromStart("", start), "an empty fingerprint must not compose '-<ts>'")
	assert.Empty(t, FiringKeyFromStart("fp1", time.Time{}))

	// The key is the instant, so the same moment written with an offset keys the
	// same firing.
	ist := time.FixedZone("IST", 5*3600+1800)
	assert.Equal(t, FiringKeyFromStart("fp1", start), FiringKeyFromStart("fp1", start.In(ist)))
}

func TestFiringFindingID(t *testing.T) {
	assert.Equal(t, "fp1-1780308000", FiringFindingID("fp1", "1780308000"))
	assert.Equal(t, "fp1", FiringFindingID("fp1", ""))
}

func TestFiringFindingIDFor_NoFiringKeyKeepsEventId(t *testing.T) {
	dbms, mock := mockDB(t)

	got, err := firingFindingIDFor(dbms, "tenant-1", "acct-1", "prometheus_alertmanager_webhook",
		EventIncomingWebhook{EventId: "incident-42"})
	require.NoError(t, err)
	assert.Equal(t, "incident-42", got)
	assert.NoError(t, mock.ExpectationsWereMet(), "a parser without a firing key must not cost a lookup")
}

// The row the lookup finds wins: the alert's open row (repeat notification, or a
// startsAt that drifted after a Prometheus restart), or the closed row of this
// same firing (late repeat after resolve, or a close made by hand).
func TestFiringFindingIDFor_ReusesFoundRow(t *testing.T) {
	dbms, mock := mockDB(t)
	start := time.Date(2026, 6, 1, 10, 0, 0, 250_000_000, time.UTC)

	mock.ExpectQuery("SELECT finding_id FROM events").
		WithArgs("fp1", "acct-1", "tenant-1", "prometheus_alertmanager_webhook",
			sameWallClock{start.Truncate(time.Second)}, sameWallClock{start.Truncate(time.Second).Add(time.Second)}).
		WillReturnRows(sqlmock.NewRows([]string{"finding_id"}).AddRow("fp1"))

	got, err := firingFindingIDFor(dbms, "tenant-1", "acct-1", "prometheus_alertmanager_webhook", firingDelivery(start))
	require.NoError(t, err)
	assert.Equal(t, "fp1", got, "a row written before per-firing ids keeps receiving its firing")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// No open row and no row for this start: the alert fired again after its last row
// closed. This is the case that used to be swallowed by the closed row.
func TestFiringFindingIDFor_MintsForNewFiring(t *testing.T) {
	dbms, mock := mockDB(t)
	start := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT finding_id FROM events").WillReturnRows(sqlmock.NewRows([]string{"finding_id"}))

	got, err := firingFindingIDFor(dbms, "tenant-1", "acct-1", "prometheus_alertmanager_webhook", firingDelivery(start))
	require.NoError(t, err)
	assert.Equal(t, "fp1-1780311600", got)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// A payload with an offset is stored as its own wall clock, so the lookup range
// must keep that offset rather than normalising to UTC.
func TestFiringFindingIDFor_KeepsPayloadOffsetInRange(t *testing.T) {
	dbms, mock := mockDB(t)
	ist := time.FixedZone("IST", 5*3600+1800)
	start := time.Date(2026, 6, 1, 15, 30, 0, 250_000_000, ist)

	mock.ExpectQuery("SELECT finding_id FROM events").
		WithArgs("fp1", "acct-1", "tenant-1", "grafana_webhook",
			sameWallClock{time.Date(2026, 6, 1, 15, 30, 0, 0, ist)}, sameWallClock{time.Date(2026, 6, 1, 15, 30, 1, 0, ist)}).
		WillReturnRows(sqlmock.NewRows([]string{"finding_id"}))

	_, err := firingFindingIDFor(dbms, "tenant-1", "acct-1", "grafana_webhook", firingDelivery(start))
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// A failed lookup mints: rows for these sources are keyed by the minted id, so it
// is the id that still finds the current firing's row.
func TestFiringFindingIDFor_LookupErrorMints(t *testing.T) {
	dbms, mock := mockDB(t)
	start := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT finding_id FROM events").WillReturnError(errors.New("connection reset"))

	got, err := firingFindingIDFor(dbms, "tenant-1", "acct-1", "prometheus_alertmanager_webhook", firingDelivery(start))
	require.Error(t, err)
	assert.Equal(t, "fp1-1780311600", got)
}
