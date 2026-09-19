package core

import (
	"net/http"
	"net/http/httptest"
	"nudgebee/llm/common"
	"nudgebee/llm/config"
	"nudgebee/llm/security"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withRAGServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := config.Config.RAGServerUrl
	config.Config.RAGServerUrl = server.URL
	t.Cleanup(func() { config.Config.RAGServerUrl = previous })
}

func TestGetRAGDecodesDataEnvelope(t *testing.T) {
	withRAGServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/collections/int-1_knowledge_base/documents", r.URL.Path)
		assert.Equal(t, "cursor/1", r.URL.Query().Get("offset"))
		_, _ = w.Write([]byte(`{"data":{"items":[{"id":"a","title":null,"url":"https://wiki/a"}],"next_offset":"b"}}`))
	})

	var page struct {
		Items      []ragKBDocument `json:"items"`
		NextOffset *string         `json:"next_offset"`
	}
	found, err := getRAG("/collections/int-1_knowledge_base/documents?offset=cursor%2F1", &page)
	require.NoError(t, err)
	assert.True(t, found)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "Runbooks", documentTitle(page.Items[0], "Runbooks"))
	require.NotNil(t, page.NextOffset)
	assert.Equal(t, "b", *page.NextOffset)
}

func TestGetRAGNotFoundAndServerError(t *testing.T) {
	status := http.StatusNotFound
	withRAGServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	})

	var out map[string]any
	found, err := getRAG("/collections/kb_1/documents", &out)
	require.NoError(t, err)
	assert.False(t, found)

	status = http.StatusInternalServerError
	_, err = getRAG("/collections/kb_1/documents", &out)
	assert.Error(t, err)
}

func TestDocumentTitlePrefersStoredTitle(t *testing.T) {
	title := "Reset VPN"
	assert.Equal(t, "Reset VPN", documentTitle(ragKBDocument{Title: &title}, "ServiceNow KB"))
	empty := ""
	assert.Equal(t, "ServiceNow KB", documentTitle(ragKBDocument{Title: &empty}, "ServiceNow KB"))
}

func TestGetKBDocumentSourceServesTheUploadedFile(t *testing.T) {
	// An uploaded document is its own source, and llm-server already holds it — asking
	// rag-server for it would mean a second copy of a file we stored ourselves.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	oldDatabase := knowledgeDatabase
	knowledgeDatabase = func(common.DatabaseManagerType) (*common.DatabaseManager, error) {
		return &common.DatabaseManager{Db: sqlx.NewDb(db, "postgresql")}, nil
	}
	t.Cleanup(func() { knowledgeDatabase = oldDatabase })

	mock.ExpectQuery(`SELECT name, .*FROM llm_knowledgebases`).
		WithArgs("kb-1", "account-a").
		WillReturnRows(sqlmock.NewRows([]string{"name", "data_filename", "data_format", "data", "updated_at"}).
			AddRow("Disk runbook", "disk.pdf", "pdf", "JVBERi0xLjcK", "2026-09-19 10:00:00"))

	// No content hash: an uploaded document has one source, not versions.
	got, err := GetKBDocumentSource(security.NewRequestContextForSuperAdmin(), "account-a", "kb-1", "")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk.pdf", got.Title)
	assert.Equal(t, "application/pdf", got.ContentType)
	assert.Equal(t, "JVBERi0xLjcK", got.Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetKBDocumentSourceFallsThroughForAScrapedPage(t *testing.T) {
	// A scraped page exists nowhere else once converted, so its source comes from
	// rag-server — and identifying which version needs the content hash.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	oldDatabase := knowledgeDatabase
	knowledgeDatabase = func(common.DatabaseManagerType) (*common.DatabaseManager, error) {
		return &common.DatabaseManager{Db: sqlx.NewDb(db, "postgresql")}, nil
	}
	t.Cleanup(func() { knowledgeDatabase = oldDatabase })

	mock.ExpectQuery(`SELECT name, .*FROM llm_knowledgebases`).
		WithArgs("kb-2", "account-a").
		WillReturnRows(sqlmock.NewRows([]string{"name", "data_filename", "data_format", "data", "updated_at"}).
			AddRow("Confluence", "", "text", "", "2026-09-19 10:00:00"))

	_, err = GetKBDocumentSource(security.NewRequestContextForSuperAdmin(), "account-a", "kb-2", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "content_hash is required")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetKBDocumentSourceServesAnUploadedTextFile(t *testing.T) {
	// rag-server keeps the source of scraped pages only, so a text upload has to be
	// served from here too — falling through would 404 a file we are holding.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	oldDatabase := knowledgeDatabase
	knowledgeDatabase = func(common.DatabaseManagerType) (*common.DatabaseManager, error) {
		return &common.DatabaseManager{Db: sqlx.NewDb(db, "postgresql")}, nil
	}
	t.Cleanup(func() { knowledgeDatabase = oldDatabase })

	mock.ExpectQuery(`SELECT name, .*FROM llm_knowledgebases`).
		WithArgs("kb-3", "account-a").
		WillReturnRows(sqlmock.NewRows([]string{"name", "data_filename", "data_format", "data", "updated_at"}).
			AddRow("Escalation policy", "escalation.txt", "text", "Page the on-call SRE.", "2026-09-19 10:00:00"))

	got, err := GetKBDocumentSource(security.NewRequestContextForSuperAdmin(), "account-a", "kb-3", "")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "escalation.txt", got.Title)
	assert.Equal(t, "text/plain", got.ContentType)
	assert.Equal(t, "Page the on-call SRE.", got.Content)
	require.NoError(t, mock.ExpectationsWereMet())
}
