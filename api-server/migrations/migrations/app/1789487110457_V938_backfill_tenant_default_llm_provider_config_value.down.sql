-- No-op: the backfilled rows cannot be told apart from ones set through the UI
-- afterwards, and a default_llm_provider config value is ignored by code that
-- predates it, so leaving them in place is safe.
SELECT 1;
