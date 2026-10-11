package core

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateConversationMessage_QueryAndCasting(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	messageID := uuid.New().String()

	// Ensure the query includes the explicit casts to public.llm_conversation_status
	expectedQuery := `UPDATE llm_conversation_messages SET response = $2, updated_at = now(), status = $3::public.llm_conversation_status, worker_name = $4, responded_at = CASE WHEN $5::boolean THEN COALESCE(responded_at, now()) ELSE NULL END WHERE id = $1 AND (status NOT IN ('TERMINATED', 'KILLED') OR $3::public.llm_conversation_status IN ('TERMINATED', 'KILLED'))`

	mock.ExpectExec(regexp.QuoteMeta(expectedQuery)).
		WithArgs(messageID, "some response", string(ConversationStatusCompleted), sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := dao.UpdateConversationMessage(messageID, "some response", ConversationStatusCompleted)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateConversationMessage_NonTerminalStatus(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	messageID := uuid.New().String()

	expectedQuery := `UPDATE llm_conversation_messages SET response = $2, updated_at = now(), status = $3::public.llm_conversation_status, worker_name = $4, responded_at = CASE WHEN $5::boolean THEN COALESCE(responded_at, now()) ELSE NULL END WHERE id = $1 AND (status NOT IN ('TERMINATED', 'KILLED') OR $3::public.llm_conversation_status IN ('TERMINATED', 'KILLED'))`

	mock.ExpectExec(regexp.QuoteMeta(expectedQuery)).
		WithArgs(messageID, "", string(ConversationStatusInProgress), sqlmock.AnyArg(), false).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := dao.UpdateConversationMessage(messageID, "", ConversationStatusInProgress)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
