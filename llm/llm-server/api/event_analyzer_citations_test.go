package api

import (
	"errors"
	"testing"

	"nudgebee/llm/common"
	"nudgebee/llm/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCitationsTestDB(t *testing.T) (*common.DatabaseManager, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return &common.DatabaseManager{Db: sqlx.NewDb(rawDB, "postgres")}, mock
}

// The pipeline passes its session id ("event-<fingerprint>"), not a conversation
// UUID. The first version of this lookup required a UUID and filtered on
// conversation_id, so it returned nothing on every real run (#38488 follow-up,
// observed on dev event 9846dfc3).
func TestCollectInvestigationCodeCitations_ResolvesConversationBySession(t *testing.T) {
	db, mock := newCitationsTestDB(t)
	ctx := security.NewRequestContextForSuperAdmin()

	explore := `{"answer":"init falls back to memstore","citations":[` +
		`{"file_path":"svc/gateway/ratelimit/builder.go","line_start":28,"line_end":32,"note":"fallback","snippet":"counters = ratelimit.NewMemStore()"},` +
		`{"file_path":"unrelated/other.go","line_start":1,"line_end":2,"snippet":"x"}]}`

	mock.ExpectQuery(`c\.session_id = \$1 AND c\.account_id = \$2 AND m\.agent_name = \$3`).
		WithArgs("event-abc123", "acct-1", "k8s_orchestrator").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(explore))

	got := collectInvestigationCodeCitations(ctx, db, "event-abc123", "acct-1", "k8s_orchestrator",
		[]string{"gateway/ratelimit/builder.go"})

	assert.Contains(t, got, "svc/gateway/ratelimit/builder.go`:28-32")
	assert.Contains(t, got, "ratelimit.NewMemStore()")
	assert.NotContains(t, got, "unrelated/other.go", "citations for files the plan did not name are dropped")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCollectInvestigationCodeCitations_DegradesToEmpty(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	t.Run("missing inputs skip the query", func(t *testing.T) {
		db, mock := newCitationsTestDB(t)
		assert.Empty(t, collectInvestigationCodeCitations(ctx, db, "", "acct-1", "k8s_orchestrator", nil))
		assert.Empty(t, collectInvestigationCodeCitations(ctx, db, "event-abc", "", "k8s_orchestrator", nil))
		assert.Empty(t, collectInvestigationCodeCitations(ctx, nil, "event-abc", "acct-1", "k8s_orchestrator", nil))
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("query error yields no evidence", func(t *testing.T) {
		db, mock := newCitationsTestDB(t)
		mock.ExpectQuery(`FROM llm_conversation_agent`).WillReturnError(errors.New("boom"))
		assert.Empty(t, collectInvestigationCodeCitations(ctx, db, "event-abc", "acct-1", "k8s_orchestrator", nil))
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("unparseable response is skipped", func(t *testing.T) {
		db, mock := newCitationsTestDB(t)
		mock.ExpectQuery(`FROM llm_conversation_agent`).
			WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow("not json"))
		assert.Empty(t, collectInvestigationCodeCitations(ctx, db, "event-abc", "acct-1", "k8s_orchestrator", nil))
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
