-- Migration: Knowledge-graph build request queue
-- Version: V939
-- Description: Durable, cross-replica record of pending/running KG builds.
--
-- Replaces the 1-hour time-window claim on knowledge_graph_tenant_filters
-- (last_process_started_at), which DROPPED any second message for a tenant that
-- arrived inside the window. With account-scoped builds that is wrong: a rebuild
-- request for account B must not be discarded because account A was just built.
--
-- Rows are inserted by the queue consumer, claimed as a batch under a per-tenant
-- advisory lock, and drained by the worker that holds the claim — so a burst of
-- messages for one tenant coalesces into a single build whose scope is the union
-- of the queued scopes. account_ids = '[]' means tenant-wide.
--
-- Lock window: single-statement DDL on a new table; no lock on existing tables.

CREATE TABLE IF NOT EXISTS public.knowledge_graph_build_requests (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL,

    -- Build scope. Empty array = tenant-wide (every account, every source).
    account_ids     jsonb NOT NULL DEFAULT '[]'::jsonb,
    sources         jsonb NOT NULL DEFAULT '[]'::jsonb,
    flow_sources    jsonb NOT NULL DEFAULT '[]'::jsonb,

    -- Provenance: what asked for this build, and the trace correlation id.
    trigger         text NOT NULL DEFAULT 'unknown',
    correlation_id  text,

    status          text NOT NULL DEFAULT 'pending',
    requested_at    timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz,
    error           text,

    CONSTRAINT kg_build_requests_status_check
        CHECK (status IN ('pending', 'running', 'done', 'failed'))
);

-- The only hot query: "is a build running / is anything pending for this tenant".
CREATE INDEX IF NOT EXISTS idx_kg_build_requests_tenant_open
    ON public.knowledge_graph_build_requests (tenant_id, status)
    WHERE status IN ('pending', 'running');

-- Supports pruning terminal rows by age.
CREATE INDEX IF NOT EXISTS idx_kg_build_requests_finished_at
    ON public.knowledge_graph_build_requests (finished_at);

COMMENT ON TABLE public.knowledge_graph_build_requests IS
    'Queue of knowledge-graph build requests. Claimed per tenant under a pg advisory lock; pending rows for a tenant are coalesced into one build (union of scopes). account_ids = [] means tenant-wide.';
