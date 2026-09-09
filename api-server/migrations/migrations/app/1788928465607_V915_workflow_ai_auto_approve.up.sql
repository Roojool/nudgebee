-- Unattended AI invocation of an automation.
--
-- ai_invocable (V838) lets the AI assistant propose an automation; the run
-- still pauses on an in-chat confirmation card. ai_auto_approve drops that
-- pause for automations whose author has decided the assistant may just run
-- them. It never widens WHAT the assistant may run: assertAIInvocationAllowed
-- and the caller's own RBAC are unchanged, and this column is meaningless
-- unless ai_invocable is also true (enforced in model validation, and by the
-- two service paths that clear ai_invocable clearing this alongside it).
--
-- A COLUMN and not a field in the versioned `definition` JSONB, for the same
-- reason V838 gives for ai_invocable: withdrawing consent to unattended runs
-- must take effect immediately, survive a version rollback, and not wait on a
-- republish.

ALTER TABLE workflows
    ADD COLUMN IF NOT EXISTS ai_auto_approve boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN workflows.ai_auto_approve IS
    'Opt-in: may the AI assistant run this workflow without asking the user to confirm. Only meaningful with ai_invocable = true. Workflow-level, not versioned, so revocation is immediate.';
