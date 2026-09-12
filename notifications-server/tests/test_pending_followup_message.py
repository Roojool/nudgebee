"""Unit tests for the pending-follow-up rejection message in ``bot_messages``.

llm-server rejects a fresh message on a conversation whose previous turn is
still WAITING on a follow-up answer with ErrConversationPendingFollowup:

    {"errors": [{"message": "conversation: previous turn is waiting on a
    followup answer; answer or cancel it before starting a new turn"}]}

Previously this didn't match ``is_conversation_in_progress_error`` (no "in
progress" / "already") and fell through to the generic "Nubi's offline"
message, even though the conversation isn't offline or busy — it's stalled
waiting on the user to answer an earlier question in the same thread.
"""

from notifications_server.services.bot_messages import (
    LLM_BUSY_MESSAGES,
    LLM_OFFLINE_MESSAGES,
    LLM_PENDING_FOLLOWUP_MESSAGE,
    get_llm_pending_followup_message,
    is_conversation_in_progress_error,
    is_conversation_pending_followup_error,
)
from notifications_server.services.events import Events

_PENDING_FOLLOWUP_BODY = {
    "errors": [
        {
            "message": (
                "conversation: previous turn is waiting on a followup answer; "
                "answer or cancel it before starting a new turn"
            )
        }
    ]
}


def test_detects_pending_followup_error():
    assert is_conversation_pending_followup_error(_PENDING_FOLLOWUP_BODY) is True


def test_ignores_non_pending_and_empty_errors():
    assert is_conversation_pending_followup_error(None) is False
    assert is_conversation_pending_followup_error({}) is False
    assert (
        is_conversation_pending_followup_error({"errors": [{"message": "conversation already in progress"}]}) is False
    )


def test_pending_followup_and_in_progress_are_mutually_exclusive():
    # The plain "already in progress" guard must not be misclassified as a
    # pending-followup rejection, and vice versa.
    assert is_conversation_in_progress_error(_PENDING_FOLLOWUP_BODY) is False
    assert (
        is_conversation_pending_followup_error(
            {"errors": [{"message": "conversation: conversation is already in progress"}]}
        )
        is False
    )


def test_message_is_not_the_offline_fallback():
    msg = get_llm_pending_followup_message()
    assert msg == LLM_PENDING_FOLLOWUP_MESSAGE
    assert msg not in LLM_OFFLINE_MESSAGES


def test_llm_error_reply_picks_pending_followup_message():
    msg = Events._llm_error_reply(400, _PENDING_FOLLOWUP_BODY)
    assert msg == LLM_PENDING_FOLLOWUP_MESSAGE


def test_llm_error_reply_still_picks_busy_message_for_plain_in_progress():
    body = {"errors": [{"message": "conversation: conversation is already in progress"}]}
    msg = Events._llm_error_reply(500, body)
    assert msg in LLM_BUSY_MESSAGES
