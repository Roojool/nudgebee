ALTER TABLE "public"."pr_followup"
    DROP COLUMN IF EXISTS "run_token",
    DROP COLUMN IF EXISTS "run_conversation_id",
    DROP COLUMN IF EXISTS "run_heartbeat_at";
