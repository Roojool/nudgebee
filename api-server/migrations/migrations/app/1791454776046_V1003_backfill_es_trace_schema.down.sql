-- Remove the backfilled trace_schema values.
--
-- This drops EVERY trace_schema row on a user ES integration, not just the ones
-- this migration inserted — a value an operator chose in the UI after the fact is
-- indistinguishable from a backfilled one. That is the right trade for a rollback:
-- the code being rolled back to infers the layout from the index name, so it does
-- not read this key at all, and leaving rows behind would make the forward
-- migration's NOT EXISTS guard skip them on re-apply.

DELETE FROM integration_config_values icv
USING integrations i
WHERE icv.integration_id = i.id
  AND icv.name = 'trace_schema'
  AND i.type = 'ES'
  AND i.source = 'user';

-- And strip trace_schema back out of each per-account index_account_mapping entry.
-- Same per-row exception handling as the forward migration: a malformed blob must
-- not abort the rollback.

DO $$
DECLARE
    r       RECORD;
    parsed  jsonb;
    rebuilt jsonb;
    elem    jsonb;
BEGIN
    FOR r IN
        SELECT v.id, v.value
        FROM integration_config_values v
        JOIN integrations i ON i.id = v.integration_id
        WHERE i.type = 'ES'
          AND i.source = 'user'
          AND v.name = 'index_account_mapping'
          AND v.value LIKE '%trace_schema%'
    LOOP
        BEGIN
            parsed := r.value::jsonb;
        EXCEPTION WHEN others THEN
            CONTINUE;
        END;

        IF jsonb_typeof(parsed) <> 'array' THEN
            CONTINUE;
        END IF;

        rebuilt := '[]'::jsonb;
        FOR elem IN SELECT * FROM jsonb_array_elements(parsed)
        LOOP
            -- `jsonb - text` is only defined for an object or an array: on a scalar
            -- element it raises "cannot delete from scalar" and aborts the rollback.
            -- A hand-edited blob like ["…", {"account_id":"A","trace_schema":"…"}]
            -- is valid JSON and reaches here, so the element type is checked rather
            -- than assumed. Reproduced before fixing; see the matching guard in the
            -- up migration.
            IF jsonb_typeof(elem) = 'object' THEN
                rebuilt := rebuilt || jsonb_build_array(elem - 'trace_schema');
            ELSE
                rebuilt := rebuilt || jsonb_build_array(elem);
            END IF;
        END LOOP;

        IF rebuilt <> parsed THEN
            UPDATE integration_config_values
            SET value = rebuilt::text, updated_at = now()
            WHERE id = r.id;
        END IF;
    END LOOP;
END $$;
