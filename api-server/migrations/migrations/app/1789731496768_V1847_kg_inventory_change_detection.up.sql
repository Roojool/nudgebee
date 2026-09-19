-- Migration: Knowledge-graph inventory change detection
-- Version: V945
-- Description: Everything the 10-minute inventory change detector and the
-- per-resource graph update need: the per-account watermark table the detector
-- compares against, the index that makes its probe cheap, and the provider-id
-- indexes the per-resource update uses to find a resource's node.
--
-- Each mark column has the same type as the inventory column it mirrors
-- (cloud_resourses.created_at, k8s_workloads.creation_time and
-- k8s_nodes.node_creation_time are timestamp WITHOUT time zone;
-- k8s_workloads.last_deployed_time is timestamptz). A mark is only ever compared
-- with its own column, never with a timestamptz from another table.
--
-- Locks. Pre-apply the four indexes out-of-band on each SaaS database with
-- CREATE INDEX CONCURRENTLY IF NOT EXISTS (same definitions) and check they are
-- present and valid (the PR lists the query); there every statement below is a
-- no-op. Where they were not pre-applied (on-prem, which runs this file from the
-- Helm hook), Atlas runs the whole file as one transaction: each CREATE INDEX
-- takes a SHARE lock on its table and keeps it until the file commits, so
-- writes to cloud_resourses and to knowledge_graph_node both wait until the
-- last of the four builds finishes; reads continue. How long that is depends on
-- those two tables' size on the install and has not been measured on-prem.

-- Per-account marks the detector compares against every 10 minutes to decide
-- which accounts need a knowledge-graph rebuild.
CREATE TABLE IF NOT EXISTS public.knowledge_graph_inventory_marks (
    cloud_account_id   uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,

    cloud_active_count integer,
    cloud_created_mark timestamp,

    k8s_active_count   integer,
    k8s_created_mark   timestamp,
    k8s_deployed_mark  timestamptz,

    node_count         integer,
    node_created_mark  timestamp,

    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kg_inventory_marks_tenant
    ON public.knowledge_graph_inventory_marks (tenant_id);

-- Serves the detector's per-account count(*) and max(created_at) over active
-- cloud resources.
CREATE INDEX IF NOT EXISTS idx_cloud_resourses_acct_created_active
    ON public.cloud_resourses (account, created_at)
    WHERE is_active;

-- Per-resource knowledge-graph updates find the node for a cloud or k8s
-- resource by its provider id (properties.resource_id, nb_resource_id or arn).
-- Partial on active tenant-level nodes so the indexes stay small.
CREATE INDEX IF NOT EXISTS idx_kg_node_active_resource_id
    ON public.knowledge_graph_node (tenant_id, cloud_account_id, (properties->>'resource_id'))
    WHERE is_active AND level = 'Tenant';

CREATE INDEX IF NOT EXISTS idx_kg_node_active_nb_resource_id
    ON public.knowledge_graph_node (tenant_id, cloud_account_id, (properties->>'nb_resource_id'))
    WHERE is_active AND level = 'Tenant';

CREATE INDEX IF NOT EXISTS idx_kg_node_active_arn_lower
    ON public.knowledge_graph_node (tenant_id, cloud_account_id, lower(properties->>'arn'))
    WHERE is_active AND level = 'Tenant';
