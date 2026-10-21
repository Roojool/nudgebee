package core

import (
	"testing"

	"nudgebee/llm/common"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OSS has no memory-v2 tables, so without the EE hook the references query must not name one.
func TestListAgentReferences_DefaultQueryNamesNoMemoryV2Table(t *testing.T) {
	var query string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		query = actual
		return nil
	})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	dao := &ConversationDao{dbManager: &common.DatabaseManager{Db: sqlx.NewDb(db, "postgresql")}}

	_, err = dao.ListAgentReferences("account-1", "", nil, "", 10)

	require.NoError(t, err)
	assert.NotContains(t, query, "llm_memory_")
	assert.Contains(t, query, "'memory_patterns','memory_decisions','memory_collective','memory_preferences'")
}
