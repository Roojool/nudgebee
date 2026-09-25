package core

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"nudgebee/services/internal/database"
	"nudgebee/services/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DatabaseManager.Exec renumbers $N placeholders in textual order and reorders
// the args to match, so the mock sees (status, error, id).
const updateStatusSQL = `UPDATE event_incoming_webhooks SET processing_status = \$1, processing_error = \$2 WHERE id = \$3`

type capturedString struct{ value string }

func (c *capturedString) Match(v driver.Value) bool {
	s, ok := v.(string)
	c.value = s
	return ok
}

// A failed delivery must carry its reason in processing_error; before this the
// row said only 'failed' and the reason lived in the service log alone.
func TestProcessStoredWebhookRecordsFailureReason(t *testing.T) {
	mock := mockMetastoreForRecovery(t)

	mock.ExpectExec("UPDATE event_incoming_webhooks SET processing_status = 'processing'").
		WithArgs("row-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, tenant_id").WithArgs("row-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "account_id", "integration_id", "integration_type", "raw", "request_url"}).
			AddRow("row-1", "tenant-1", "", "integration-1", "no_such_webhook", "{}", ""))
	// Building the tenant-admin context looks up the tenant's accounts.
	mock.ExpectQuery("SELECT id FROM cloud_accounts").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(updateStatusSQL).
		WithArgs(webhookProcessingStatusFailed, "integrations: integration type no_such_webhook not found", "row-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := ProcessStoredWebhook(security.NewRequestContextForSuperAdmin(nil, nil, nil), "row-1")
	require.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateWebhookStatusClearsErrorOnSuccess(t *testing.T) {
	mock := mockMetastoreForRecovery(t)
	dbms, err := database.GetDatabaseManager(database.Metastore)
	require.NoError(t, err)

	mock.ExpectExec(updateStatusSQL).
		WithArgs(webhookProcessingStatusProcessed, nil, "row-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updateWebhookStatus(dbms, "row-1", webhookProcessingStatusProcessed, nil)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateWebhookStatusTruncatesToValidUTF8(t *testing.T) {
	mock := mockMetastoreForRecovery(t)
	dbms, err := database.GetDatabaseManager(database.Metastore)
	require.NoError(t, err)

	// "é" is two bytes, so a byte-offset cut after the leading "x" lands inside a rune.
	long := "x" + strings.Repeat("é", maxWebhookProcessingErrorLen)
	stored := &capturedString{}
	mock.ExpectExec(updateStatusSQL).
		WithArgs(webhookProcessingStatusFailed, stored, "row-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updateWebhookStatus(dbms, "row-1", webhookProcessingStatusFailed, errors.New(long))
	require.NoError(t, mock.ExpectationsWereMet())

	assert.True(t, utf8.ValidString(stored.value))
	assert.LessOrEqual(t, len(stored.value), maxWebhookProcessingErrorLen)
	assert.True(t, strings.HasPrefix(long, stored.value))
}

// A short message is not truncated, but a parser error can still echo raw
// payload bytes that Postgres would reject.
func TestUpdateWebhookStatusSanitizesShortMessage(t *testing.T) {
	mock := mockMetastoreForRecovery(t)
	dbms, err := database.GetDatabaseManager(database.Metastore)
	require.NoError(t, err)

	stored := &capturedString{}
	mock.ExpectExec(updateStatusSQL).
		WithArgs(webhookProcessingStatusFailed, stored, "row-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updateWebhookStatus(dbms, "row-1", webhookProcessingStatusFailed, errors.New("bad \xff byte\x00 here"))
	require.NoError(t, mock.ExpectationsWereMet())

	assert.Equal(t, "bad  byte here", stored.value)
}
