-- Repair the specific application tables found in the nudgebee role schema
-- during upgrades from older charts. Keep the three migration bookkeeping
-- tables in nudgebee. SET SCHEMA preserves rows, indexes, foreign keys and
-- table OIDs; one transaction makes the move all-or-nothing for live readers.
BEGIN;
SET LOCAL lock_timeout = '5s';

DO $$
DECLARE
    misplaced record;
BEGIN
    IF current_user <> 'nudgebee' THEN
        RAISE EXCEPTION 'role-schema reconciliation requires the nudgebee role';
    END IF;

    -- Serialize overlapping Helm hooks before taking a catalog snapshot.
    PERFORM pg_advisory_xact_lock(39267, 1);

    FOR misplaced IN
        SELECT c.relname, c.relowner
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'nudgebee'
          AND c.relkind IN ('r', 'p')
          AND c.relname = ANY (ARRAY[
              'llm_conversation_attachment_refs',
              'llm_conversation_attachments',
              'llm_gateway_rate_limits',
              'llm_gateway_request_log',
              'llm_gateway_routing_rules',
              'llm_gateway_tenant_settings',
              'llm_gateway_usage',
              'llm_memory_user_consent',
              'llm_session_working_memory',
              'messaging_channel_message',
              'messaging_channel_watch',
              'triage_correction_log',
              'triage_signal_class',
              'webhook_subject_mappings',
              'workflow_versions'
          ])
        ORDER BY c.relname
    LOOP
        IF misplaced.relowner <> (SELECT oid FROM pg_roles WHERE rolname = current_user) THEN
            RAISE EXCEPTION 'nudgebee.% is not owned by the migration role', misplaced.relname;
        END IF;
        IF to_regclass(format('public.%I', misplaced.relname)) IS NOT NULL THEN
            RAISE EXCEPTION 'both public.% and nudgebee.% exist; inspect before moving',
                misplaced.relname, misplaced.relname;
        END IF;
        IF NOT has_schema_privilege(current_user, 'public', 'CREATE') THEN
            RAISE EXCEPTION 'migration role cannot create objects in public';
        END IF;

        EXECUTE format('ALTER TABLE nudgebee.%I SET SCHEMA public', misplaced.relname);
        RAISE NOTICE 'moved nudgebee.% to public', misplaced.relname;
    END LOOP;
END $$;

COMMIT;
