package vmpackage

import (
	"fmt"
	"time"

	"nudgebee/services/internal/database"

	"github.com/google/uuid"
)

// Scan status values written to vm_scan_status.status (see V962 migration).
const (
	ScanStatusStarted   = "started"
	ScanStatusCompleted = "completed"
	ScanStatusFailed    = "failed"
)

// maxScanErrorLen caps the stored error text so a verbose wrapped error
// chain (e.g. a relay/SSH failure with a full command dump) can't make a
// single row unreasonably large. Mirrors scan_orchestrator's image scan
// summary, which caps its own error field the same way.
const maxScanErrorLen = 500

// startScanAttempt claims vm_scan_status for a new scan attempt on
// cloudResourceID and returns an attempt id the caller must pass to
// finishScanAttempt. Called once at the start of every scan path (manual,
// cron sweep, queue consumer).
//
// Each call unconditionally claims the row (status=started, a fresh id):
// starting a new scan always means "this is now the attempt to track".
// finishScanAttempt's terminal write, however, only lands if its attempt id
// still matches — so if an older attempt is still running when a newer,
// overlapping one starts (e.g. a manual per-VM scan racing the cron/queue
// sweep on the same VM), the older attempt finishing later can't stomp the
// newer attempt's still-in-progress status. Best-effort: a DB hiccup here
// must never fail or unwind the scan itself, so errors are swallowed and
// the caller still gets a usable (if unpersisted) attempt id.
func startScanAttempt(tenantID, cloudAccountID, cloudResourceID string) string {
	attemptID := uuid.New().String()
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return attemptID
	}
	_, _ = dbms.Db.Exec(
		`INSERT INTO vm_scan_status
		   (cloud_resource_id, tenant_id, cloud_account_id, status, attempt_id, error, package_count, finding_count, scanned_at, updated_at)
		 VALUES
		   ($1, $2, $3, $4, $5, '', 0, 0, NOW(), NOW())
		 ON CONFLICT (cloud_resource_id)
		 DO UPDATE SET tenant_id = EXCLUDED.tenant_id, cloud_account_id = EXCLUDED.cloud_account_id,
		               status = EXCLUDED.status, attempt_id = EXCLUDED.attempt_id,
		               error = '', package_count = 0, finding_count = 0,
		               scanned_at = EXCLUDED.scanned_at, updated_at = EXCLUDED.updated_at`,
		cloudResourceID, tenantID, cloudAccountID, ScanStatusStarted, attemptID,
	)
	return attemptID
}

// finishScanAttempt records a terminal outcome (completed/failed) for one
// scan attempt. The write is scoped by attempt_id, so it silently no-ops if
// a newer attempt has since claimed the row — see startScanAttempt's doc
// comment for why that matters.
func finishScanAttempt(cloudResourceID, attemptID, status string, scanErr error, packageCount, findingCount int) {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return
	}

	errText := ""
	if scanErr != nil {
		errText = scanErr.Error()
		if len(errText) > maxScanErrorLen {
			errText = errText[:maxScanErrorLen]
		}
	}

	_, _ = dbms.Db.Exec(
		`UPDATE vm_scan_status
		 SET status = $1, error = $2, package_count = $3, finding_count = $4, scanned_at = NOW(), updated_at = NOW()
		 WHERE cloud_resource_id = $5 AND attempt_id = $6`,
		status, errText, packageCount, findingCount, cloudResourceID, attemptID,
	)
}

// ScanStatusRow is one vm_scan_status row shaped for the RPC response —
// plain map[string]any (matching ScanResponse/ScanAccountResponse's
// existing Data shape) would work too, but a struct + sqlx.Select catches a
// column/field typo at compile time instead of a silently-missing key.
type ScanStatusRow struct {
	CloudResourceID string    `db:"cloud_resource_id" json:"cloud_resource_id"`
	Status          string    `db:"status" json:"status"`
	Error           string    `db:"error" json:"error"`
	PackageCount    int       `db:"package_count" json:"package_count"`
	FindingCount    int       `db:"finding_count" json:"finding_count"`
	ScannedAt       time.Time `db:"scanned_at" json:"scanned_at"`
	UpdatedAt       time.Time `db:"updated_at" json:"updated_at"`
}

// ListScanStatusesForAccount returns every vm_scan_status row for the
// account, one per cloud_resource_id that has ever been scanned, for the
// UI to join against its VM list (see #38466 — nothing on the row today
// says whether a scan is running, finished, or failed).
func ListScanStatusesForAccount(cloudAccountID string) ([]ScanStatusRow, error) {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, fmt.Errorf("vmpackage: db: %w", err)
	}
	var rows []ScanStatusRow
	err = dbms.Db.Select(&rows,
		`SELECT cloud_resource_id, status, error, package_count, finding_count, scanned_at, updated_at
		 FROM vm_scan_status WHERE cloud_account_id = $1`, cloudAccountID)
	if err != nil {
		return nil, fmt.Errorf("vmpackage: list scan statuses for account %s: %w", cloudAccountID, err)
	}
	return rows, nil
}

// SweepStaleScanStatuses flips any vm_scan_status row still "started" past
// scanTimeout (plus a grace period for clock skew / cron cadence) to
// "failed". Without this, a scan whose goroutine never reaches a terminal
// write — the process was evicted, the pod restarted, a panic occurred
// somewhere finishScanAttempt itself can't guard against — would leave the
// UI showing "Scanning..." forever. Called from its own "VM Scan Status
// Sweep" cron trigger (runbook-server/internal/system/cron_triggers.yaml),
// every 5 minutes — independent of the once-daily "VM Vulnerability Scan"
// job, so a stuck scan is caught within the scan timeout, not up to a day
// later.
func SweepStaleScanStatuses() error {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return fmt.Errorf("vmpackage: db: %w", err)
	}
	cutoff := time.Now().Add(-(scanTimeout + 5*time.Minute))
	_, err = dbms.Db.Exec(
		`UPDATE vm_scan_status SET status = $1, error = $2, updated_at = NOW()
		 WHERE status = $3 AND updated_at < $4`,
		ScanStatusFailed, "scan did not report back", ScanStatusStarted, cutoff,
	)
	if err != nil {
		return fmt.Errorf("vmpackage: sweep stale scan statuses: %w", err)
	}
	return nil
}
