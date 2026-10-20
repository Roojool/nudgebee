-- Reverse V921. The compatibility view goes first: it depends on the table and
-- on the candidates column, so neither can be renamed back while it exists.
--
-- score is restored to NOT NULL, which requires the rows written under V921 to
-- have a value. They deliberately leave it NULL, so backfill from the highest
-- weighted score in candidates before reimposing the constraint; rows with no
-- usable candidates fall back to 0 rather than blocking the rollback.

DROP VIEW IF EXISTS "public"."llm_rag_audit";

DROP INDEX IF EXISTS "public"."idx_retrieval_trace_module_time";
DROP INDEX IF EXISTS "public"."idx_retrieval_trace_account_time";
DROP INDEX IF EXISTS "public"."idx_retrieval_trace_conversation";

ALTER TABLE "public"."llm_retrieval_trace"
    DROP COLUMN IF EXISTS "created_at",
    DROP COLUMN IF EXISTS "scorer_version",
    DROP COLUMN IF EXISTS "fallback_reason",
    DROP COLUMN IF EXISTS "judge_kept_ref",
    DROP COLUMN IF EXISTS "judge_score",
    DROP COLUMN IF EXISTS "judge_verdict",
    DROP COLUMN IF EXISTS "judge_ms",
    DROP COLUMN IF EXISTS "score_ms",
    DROP COLUMN IF EXISTS "retrieval_ms",
    DROP COLUMN IF EXISTS "n_injected",
    DROP COLUMN IF EXISTS "n_retrieved",
    DROP COLUMN IF EXISTS "requested_k",
    DROP COLUMN IF EXISTS "tenant_id",
    DROP COLUMN IF EXISTS "message_id";

UPDATE "public"."llm_retrieval_trace"
   SET "score" = COALESCE((
           SELECT max((c->>'weighted')::float8)
             FROM jsonb_array_elements(
                      CASE WHEN jsonb_typeof("candidates") = 'array'
                           THEN "candidates" ELSE '[]'::jsonb END
                  ) AS c
            WHERE c ? 'weighted'
       ), 0)
 WHERE "score" IS NULL;

ALTER TABLE "public"."llm_retrieval_trace" ALTER COLUMN "score" SET NOT NULL;

ALTER TABLE "public"."llm_retrieval_trace" RENAME COLUMN "candidates" TO "response";
ALTER TABLE "public"."llm_retrieval_trace" RENAME TO "llm_rag_audit";
