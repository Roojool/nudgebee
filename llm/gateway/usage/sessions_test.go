package usage

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/llm-gateway/common"
	"nudgebee/llm-gateway/config"
)

// A session whose request-log row is gone (or has no first_user_message) makes the
// LEFT JOIN LATERAL yield NULL; the preview must be COALESCEd so the scan doesn't fail.
func TestListSessions_FirstMessageNullSafe(t *testing.T) {
	prevCap, prevURL := config.Config.CaptureBody, config.Config.GatewayDBURL
	t.Cleanup(func() { config.Config.CaptureBody, config.Config.GatewayDBURL = prevCap, prevURL })
	config.Config.CaptureBody, config.Config.GatewayDBURL = true, "postgres://unused"

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db := &common.DatabaseManager{Db: sqlx.NewDb(sqlDB, "postgres")}

	now := time.Now()
	mock.ExpectQuery(`SELECT count\(DISTINCT g.session_id\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	cols := []string{"session_id", "user_id", "display_name", "username", "session_source",
		"requests", "input_tokens", "output_tokens", "cache_read_tokens", "cost_usd",
		"models", "providers", "first_seen", "last_seen", "first_message"}
	mock.ExpectQuery(regexp.QuoteMeta(`THEN COALESCE(max(fb.first_user_message),'') ELSE '' END AS first_message`)).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("s1", "u1", "", "", "header", 2, 10, 5, 0, 0.1, "m", "p", now, now, ""))

	out, err := ListSessions(context.Background(), db, ListSessionsRequest{
		TenantID: "t1", StartDate: now.Add(-time.Hour), EndDate: now, CallerUserID: "u1",
	})
	require.NoError(t, err)
	require.Len(t, out.Rows, 1)
	assert.Equal(t, "", out.Rows[0].FirstMessage)
	require.NoError(t, mock.ExpectationsWereMet())
}
