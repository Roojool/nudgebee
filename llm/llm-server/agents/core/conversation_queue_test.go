package core

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nudgebee/llm/config"
)

func TestQueuedMutationsScopeAccountAndAuthor(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(map[bool]string{false: "author", true: "admin"}[admin], func(t *testing.T) {
			dao, mock := setupConversationDAOMock(t)
			if admin {
				mock.ExpectExec(`DELETE FROM llm_conversation_messages WHERE id = \$1 AND status = \$2 AND account_id = \$3`).WithArgs("message", "QUEUED", "account").WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec(`DELETE FROM llm_conversation_messages WHERE id = \$1 AND status = \$2 AND user_id = \$3 AND account_id = \$4`).WithArgs("message", "QUEUED", "", "account").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			require.ErrorIs(t, dao.DeleteQueuedMessage("message", "account", "", admin), ErrQueuedMessageNotFoundOrLocked)
			if admin {
				mock.ExpectExec(`UPDATE llm_conversation_messages SET message = \$1, updated_at = now\(\) WHERE id = \$2 AND status = \$3 AND account_id = \$4`).WithArgs("edited", "message", "QUEUED", "account").WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec(`UPDATE llm_conversation_messages SET message = \$1, updated_at = now\(\) WHERE id = \$2 AND status = \$3 AND user_id = \$4 AND account_id = \$5`).WithArgs("edited", "message", "QUEUED", "author", "account").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			require.ErrorIs(t, dao.UpdateQueuedMessage("message", "account", "author", "edited", admin), ErrQueuedMessageNotFoundOrLocked)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestQueuedForceInvalidTargetDoesNotTerminate(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv", "account").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("IN_PROGRESS"))
	mock.ExpectQuery(`SELECT .* FROM llm_conversation_messages WHERE id = \$1 AND account_id = \$2 AND conversation_id = \$3 AND status = 'QUEUED' AND \(\$4 OR user_id::text = \$5\) FOR UPDATE`).WithArgs("message", "account", "conv", false, "author").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	msg, err := dao.ClaimQueuedMessageById(nil, "account", "conv", "message", "author", false)
	require.Nil(t, msg)
	require.ErrorIs(t, err, ErrQueuedMessageNotFoundOrLocked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedInsertChecksCapacityWhileConversationLocked(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	old := config.Config.MaxQueuedMessagesPerConversation
	config.Config.MaxQueuedMessagesPerConversation = 2
	t.Cleanup(func() { config.Config.MaxQueuedMessagesPerConversation = old })
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv", "account").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("IN_PROGRESS"))
	mock.ExpectQuery(`SELECT count\(\*\).*status = 'QUEUED'`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectRollback()
	_, err := dao.SaveConversationMessage("", "conv", "account", "author", MessageRoleHuman, MessageTypeGeneration, "question", "", "", uuid.Nil, nil, "", "", "", ConversationStatusQueued)
	require.ErrorIs(t, err, ErrQueueFull)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedInsertRejectsCompletionRace(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv", "account").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("COMPLETED"))
	mock.ExpectRollback()
	_, err := dao.SaveConversationMessage("", "conv", "account", "author", MessageRoleHuman, MessageTypeGeneration, "question", "", "", uuid.Nil, nil, "", "", "", ConversationStatusQueued)
	require.ErrorIs(t, err, ErrQueueInactive)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedClaimDoesNotStartDuringAnotherTurn(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("IN_PROGRESS"))
	mock.ExpectRollback()
	msg, err := dao.ClaimNextQueuedMessage(context.Background(), "conv")
	require.NoError(t, err)
	require.Nil(t, msg)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedClaimAdvancesAfterFailedTurn(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("FAILED"))
	// Reaching the claim query proves a FAILED turn no longer strands the queue.
	mock.ExpectQuery(`UPDATE llm_conversation_messages SET status = 'IN_PROGRESS'`).WithArgs("conv", sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	msg, err := dao.ClaimNextQueuedMessage(context.Background(), "conv")
	require.NoError(t, err)
	require.Nil(t, msg)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedClaimStaysPausedAfterStop(t *testing.T) {
	for _, status := range []string{"KILLED", "TERMINATED"} {
		t.Run(status, func(t *testing.T) {
			dao, mock := setupConversationDAOMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
			mock.ExpectRollback()
			msg, err := dao.ClaimNextQueuedMessage(context.Background(), "conv")
			require.NoError(t, err)
			require.Nil(t, msg)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestQueuedHistoryIsExcludedBeforeLimit(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	id := uuid.NewString()
	mock.ExpectQuery(`WHERE conversation_id = \$1 AND account_id = \$2 AND status <> 'QUEUED'.*ORDER BY created_at DESC LIMIT`).WithArgs(id, "account", 5).WillReturnRows(sqlmock.NewRows([]string{"role", "message", "response", "id", "message_type"}))
	_, err := dao.LoadConversationMessages("account", id, "", "", 5)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedFinalizeCannotOverwriteForcedTurn(t *testing.T) {
	for _, outcome := range []ConversationStatus{ConversationStatusCompleted, ConversationStatusFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			dao, mock := setupConversationDAOMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("IN_PROGRESS"))
			mock.ExpectQuery(`SELECT status FROM llm_conversation_messages.*FOR UPDATE`).WithArgs("old", "conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("TERMINATED"))
			mock.ExpectRollback()
			applied, err := dao.FinalizeConversationMessage("conv", "old", "late output", outcome)
			require.NoError(t, err)
			require.False(t, applied)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestQueuedFreshTurnCannotOvertakeQueue(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	old := config.Config.QueuedMessagesEnabled
	config.Config.QueuedMessagesEnabled = true
	t.Cleanup(func() { config.Config.QueuedMessagesEnabled = old })
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv", "account").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("COMPLETED"))
	mock.ExpectQuery(`SELECT EXISTS .*'QUEUED'`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()
	_, err := dao.SaveConversationMessage("", "conv", "account", "author", MessageRoleHuman, MessageTypeGeneration, "question", "", "", uuid.Nil, nil, "", "", "", ConversationStatusInProgress)
	require.ErrorContains(t, err, "active or queued")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedFinalizeAllowsOwnApprovalWait(t *testing.T) {
	dao, mock := setupConversationDAOMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM llm_conversations.*FOR UPDATE`).WithArgs("conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("IN_PROGRESS"))
	mock.ExpectQuery(`SELECT status FROM llm_conversation_messages.*FOR UPDATE`).WithArgs("message", "conv").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("IN_PROGRESS"))
	mock.ExpectExec(`UPDATE llm_conversation_messages SET response`).WithArgs("message", "Approval needed", ConversationStatusWaiting, config.Config.ServerName, false).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE llm_conversations.*AND \(\$2::public.llm_conversation_status NOT IN \('WAITING', 'WAITING_FOR_CLIENT_TOOL'\) OR message_type = 'generation'\)`).WithArgs("conv", ConversationStatusWaiting, "message").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	applied, err := dao.FinalizeConversationMessage("conv", "message", "Approval needed", ConversationStatusWaiting)
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueuedDispatchDoesNotResurrectCancelledClaim(t *testing.T) {
	cfg := additionalConversationSessionRequestConfig{}
	ConversationSessionRequestWithQueuedDispatch().apply(&cfg)
	require.True(t, cfg.isResume, "the claim already reserved the active conversation")
	require.True(t, cfg.isQueuedDispatch)
	for _, status := range []ConversationStatus{ConversationStatusTerminated, ConversationStatusKilled, ConversationStatusCompleted, ConversationStatusFailed} {
		require.True(t, shouldSkipQueuedDispatch(ConversationStatusInProgress, status), "a newer running turn must not revive the old message")
		require.True(t, shouldSkipQueuedDispatch(status, ConversationStatusInProgress))
	}
	require.False(t, shouldSkipQueuedDispatch(ConversationStatusInProgress, ConversationStatusInProgress))
	require.False(t, shouldCleanupExistingMessage(cfg.isQueuedDispatch, ConversationStatusInProgress, MessageTypeGeneration), "cleanup could race stop and reset the cancelled message")
	require.True(t, shouldCleanupExistingMessage(false, ConversationStatusInProgress, MessageTypeGeneration), "ordinary recovery still cleans up")
	require.False(t, shouldCleanupExistingMessage(false, ConversationStatusWaiting, MessageTypeGeneration))
	require.False(t, shouldCleanupExistingMessage(false, ConversationStatusInProgress, MessageTypeFollowup))
}
