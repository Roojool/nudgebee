package vmpackage

import (
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartScanAttempt_ClaimsRow(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectExec("INSERT INTO vm_scan_status").
		WithArgs("resource-1", "tenant-1", "account-1", ScanStatusStarted, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	attemptID := startScanAttempt("tenant-1", "account-1", "resource-1")
	assert.NotEmpty(t, attemptID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestFinishScanAttempt_CompletedRecordsCounts(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectExec("UPDATE vm_scan_status SET status").
		WithArgs(ScanStatusCompleted, "", 42, 3, "resource-1", "attempt-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	finishScanAttempt("resource-1", "attempt-1", ScanStatusCompleted, nil, 42, 3)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestFinishScanAttempt_FailedRecordsError(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectExec("UPDATE vm_scan_status SET status").
		WithArgs(ScanStatusFailed, "discovery_inventory: boom", 0, 0, "resource-1", "attempt-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	finishScanAttempt("resource-1", "attempt-1", ScanStatusFailed, errors.New("discovery_inventory: boom"), 0, 0)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// A wrapped error chain from a relay/SSH failure can be arbitrarily long —
// this pins the cap so one bad scan can't bloat vm_scan_status rows.
func TestFinishScanAttempt_ErrorTruncated(t *testing.T) {
	mock := withMockDB(t)
	longErr := errors.New(strings.Repeat("x", maxScanErrorLen+100))
	mock.ExpectExec("UPDATE vm_scan_status SET status").
		WithArgs(ScanStatusFailed, strings.Repeat("x", maxScanErrorLen), 0, 0, "resource-1", "attempt-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	finishScanAttempt("resource-1", "attempt-1", ScanStatusFailed, longErr, 0, 0)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Pins the concurrency contract a review of this PR raised: a terminal
// write is scoped by attempt_id, so an older attempt finishing after a
// newer, overlapping one has already claimed the row is a silent no-op
// rather than a status overwrite. sqlmock can't assert "0 rows changed the
// world", but it does assert the UPDATE is always attempt-scoped, which is
// what makes that guarantee hold at the SQL level.
func TestFinishScanAttempt_ScopedByAttemptID(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectExec("UPDATE vm_scan_status SET status").
		WithArgs(ScanStatusFailed, "stale attempt", 0, 0, "resource-1", "old-attempt").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows: a newer attempt already claimed the row.

	finishScanAttempt("resource-1", "old-attempt", ScanStatusFailed, errors.New("stale attempt"), 0, 0)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Pins the stale-scan sweep's UPDATE shape: it flips "started" rows past the
// cutoff to "failed" with a fixed reason, independent of any goroutine ever
// reaching a terminal finishScanAttempt call. See #38466.
func TestSweepStaleScanStatuses_FlipsStartedPastTimeout(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectExec("UPDATE vm_scan_status SET status").
		WithArgs(ScanStatusFailed, "scan did not report back", ScanStatusStarted, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))

	err := SweepStaleScanStatuses()
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
