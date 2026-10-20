-- Turn the write-only RAG audit into a retrieval trace both flows can use.
--
-- llm_rag_audit has taken one row per search since Oct 2024 and nothing has ever
-- read it: the only references are the writer in rag-server and the account
-- deletion sweep. It records the documents that survived filtering, so it cannot
-- answer the question that matters — what was retrieved and then dropped, and
-- why. A deliberate "none of these are relevant" verdict and a search that found
-- nothing are indistinguishable in it today.
--
-- It already spans both flows (module carries knowledge_base alongside
-- memory_patterns / memory_decisions / memory_collective / long_term_memory,
-- because the memory layers retrieve through rag-server), so this extends what
-- is there rather than adding a second table beside it.
--
-- Every added column is nullable with no default. The table is ~530k rows /
-- ~1.9GB on dev, and a volatile default such as now() would rewrite all of it
-- under an exclusive lock; nullable-no-default is a catalog-only change.
-- Existing rows keep score/response and are identified by created_at IS NULL.

-- Guarded because dev had this hand-applied before it merged; everything after
-- the renames is already idempotent.
DO $$
BEGIN
    IF to_regclass('public.llm_retrieval_trace') IS NULL THEN
        ALTER TABLE "public"."llm_rag_audit" RENAME TO "llm_retrieval_trace";
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'llm_retrieval_trace'
                 AND column_name = 'response') THEN
        ALTER TABLE "public"."llm_retrieval_trace" RENAME COLUMN "response" TO "candidates";
    END IF;
END $$;

-- score held "the top document's score". With similarity, cross-encoder,
-- weighted and judge scores now recorded per candidate, a single unlabelled
-- number has no defined meaning, so new rows leave it NULL.
ALTER TABLE "public"."llm_retrieval_trace" ALTER COLUMN "score" DROP NOT NULL;

ALTER TABLE "public"."llm_retrieval_trace"
    ADD COLUMN IF NOT EXISTS "message_id"      uuid,
    ADD COLUMN IF NOT EXISTS "tenant_id"       uuid,
    ADD COLUMN IF NOT EXISTS "requested_k"     integer,
    ADD COLUMN IF NOT EXISTS "n_retrieved"     integer,
    ADD COLUMN IF NOT EXISTS "n_injected"      integer,
    ADD COLUMN IF NOT EXISTS "retrieval_ms"    integer,
    ADD COLUMN IF NOT EXISTS "score_ms"        integer,
    ADD COLUMN IF NOT EXISTS "judge_ms"        integer,
    -- kept | none | unavailable | skipped. "unavailable" is the fail-open case
    -- and must never be read as "nothing was relevant": the candidates were
    -- kept, not rejected.
    ADD COLUMN IF NOT EXISTS "judge_verdict"   text,
    ADD COLUMN IF NOT EXISTS "judge_score"     double precision,
    -- Parent document id of the kept candidate, matching candidates[].ref.
    ADD COLUMN IF NOT EXISTS "judge_kept_ref"  text,
    ADD COLUMN IF NOT EXISTS "fallback_reason" text,
    ADD COLUMN IF NOT EXISTS "scorer_version"  text,
    ADD COLUMN IF NOT EXISTS "created_at"      timestamptz;

COMMENT ON TABLE "public"."llm_retrieval_trace" IS
    'One row per retrieval, for the knowledge-base and memory flows alike. '
    'candidates holds every candidate the search returned, kept and dropped, '
    'with its scores and the reason it was dropped. Rows with created_at IS NULL '
    'predate this and carry the old {page_content, score} shape in candidates.';

COMMENT ON COLUMN "public"."llm_retrieval_trace"."candidates" IS
    'jsonb array, one entry per candidate: rank_in, rank_out, ref, chunk, '
    'snippet, sim, rr, weighted, judge, status (injected | dropped_floor | '
    'dropped_judge | dropped_cap | dropped_keep_none), reason, prompt_chars.';

COMMENT ON COLUMN "public"."llm_retrieval_trace"."scorer_version" IS
    'Derived from live config at request time, never hand-maintained, so two '
    'configurations are always distinguishable. e.g. '
    'emb=bge-small-en-v1.5;rr=ms-marco-MiniLM-L-6-v2/128/500;score=w0.50;k=10';

COMMENT ON COLUMN "public"."llm_retrieval_trace"."fallback_reason" IS
    'Why the flow did not deliver from its scored candidates: rag_error, '
    'rag_timeout, rag_empty, hydration_empty, flag_off, keep_none. keep_none is '
    'a deliberate "nothing here is relevant" verdict, not a failure.';

-- rag-server writes `INSERT INTO llm_rag_audit` today and deploys independently
-- of this migration. The view keeps that insert working across the rename; it is
-- auto-updatable (single table, no aggregates), so no rule or trigger is needed.
-- Dropped once both writers use the new name.
CREATE OR REPLACE VIEW "public"."llm_rag_audit" AS
    SELECT "id",
           "cloud_account_id",
           "module",
           "query",
           "score",
           "candidates" AS "response",
           "conversation_id"
    FROM "public"."llm_retrieval_trace";

-- Analysis is by conversation/message, by account over time, and by module.
-- llm_rag_audit had no index but its primary key, which is part of why querying
-- it was never practical. These are plain CREATE INDEX statements, as the
-- migration linter requires; build them out of band first on any database where
-- the lock would matter, and these become no-ops there.
CREATE INDEX IF NOT EXISTS "idx_retrieval_trace_conversation"
    ON "public"."llm_retrieval_trace" ("conversation_id", "message_id");
CREATE INDEX IF NOT EXISTS "idx_retrieval_trace_account_time"
    ON "public"."llm_retrieval_trace" ("cloud_account_id", "created_at");
CREATE INDEX IF NOT EXISTS "idx_retrieval_trace_module_time"
    ON "public"."llm_retrieval_trace" ("module", "created_at");
