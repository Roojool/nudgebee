CREATE INDEX IF NOT EXISTS idx_llm_conversation_agent_account_followup
  ON llm_conversation_agent (account_id, followup_message_id);
