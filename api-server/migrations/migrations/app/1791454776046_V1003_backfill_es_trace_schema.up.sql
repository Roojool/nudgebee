-- Backfill trace_schema for every user-created Elasticsearch integration.
--
-- Elasticsearch stores traces in one of three mutually incompatible layouts, and
-- until now the reader was chosen by string-matching the configured trace index
-- name (observability.isOtelNativeTraceIndex, now deleted). That guess mis-read
-- Elastic APM: traces-apm* matched the "traces-" prefix rule and selected the
-- OTel-native reader, so every query returned zero hits. The layout is now a
-- declared property ("trace_schema") instead.
--
-- This runs the old inference ONCE, here, so existing integrations keep resolving
-- to the reader they resolve to today and nobody has to re-save an integration.
-- After this, nothing infers the layout from a name.
--
-- The CASE reproduces isOtelNativeTraceIndex exactly, with one deliberate
-- correction: traces-apm* is tested BEFORE the bare traces- prefix, so an Elastic
-- APM index now lands on 'elastic_apm' rather than being mis-typed as OTel-native.
-- That is the bug being fixed; it is the only row whose effective behaviour changes.
--
-- A blank or absent trace_index means the reader fell back to its Data Prepper
-- default (otel-v1-apm-span-*), which is why NULL maps to 'data_prepper'.
--
-- Scope: ES integrations only, and only source='user' — agent-sourced ES never
-- serves traces (getTraceSource requires integrationSource = "user"), so those
-- rows have no trace reader to select.
--
-- Idempotent via NOT EXISTS, and cheap: a few tens of rows, no DDL and no index
-- build, so the lock window is negligible. integration_config_values is an EAV
-- table with a unique (integration_id, name), so no schema change is needed to
-- introduce a new config key.
--
-- Values are btrim'd to match the deleted function, which ran strings.TrimSpace
-- before testing its prefixes. The create path trims, so a padded value is only
-- reachable for a row written another way — but " traces-apm*" classified as
-- data_prepper is exactly the silent-zero-hits failure this change exists to end.
--
-- PART 2 below handles the per-account index_account_mapping blob, which matters
-- for the same reason: GetElasticsearchConfig overwrites cfg.TraceIndex with an
-- account's override BEFORE the old heuristic ran on it, so a per-account index
-- did select the reader. Backfilling only the top-level row would regress any
-- account whose override implies a different layout.

-- PART 1 — the integration's top-level trace_schema.

INSERT INTO integration_config_values (id, integration_id, name, value, is_encrypted, created_at, updated_at)
SELECT
    gen_random_uuid(),
    i.id,
    'trace_schema',
    CASE
        -- Data Prepper's own index, named explicitly.
        WHEN btrim(tv.value) LIKE '%otel-v1-apm-span%' THEN 'data_prepper'
        -- Elastic APM Server's data stream. MUST precede the 'traces-' test below.
        WHEN btrim(tv.value) LIKE 'traces-apm%' THEN 'elastic_apm'
        -- OTel-native: a .otel data stream, or anything else under traces-.
        WHEN btrim(tv.value) LIKE '%.otel%' OR btrim(tv.value) LIKE 'traces-%' THEN 'otel_native'
        -- Blank, absent, or an unrecognised pattern: the reader defaulted to
        -- Data Prepper, so record that.
        ELSE 'data_prepper'
    END,
    false,
    now(),
    now()
FROM integrations i
LEFT JOIN integration_config_values tv
       ON tv.integration_id = i.id
      AND tv.name = 'trace_index'
WHERE i.type = 'ES'
  AND i.source = 'user'
  AND NOT EXISTS (
      SELECT 1 FROM integration_config_values icv
      WHERE icv.integration_id = i.id AND icv.name = 'trace_schema'
  );

-- PART 2 — the per-account overrides inside index_account_mapping.
--
-- Adds a trace_schema to each blob entry that sets its own trace_index but no
-- schema, derived from that entry's index by the same rules as PART 1. Entries
-- with no trace_index are left alone: they inherit the top-level index, and with
-- it the top-level schema PART 1 just wrote.
--
-- Written as a loop with a per-row exception handler rather than a set-based
-- UPDATE because value is TEXT, not jsonb: one malformed blob would abort the
-- whole migration and block the deploy. The runtime already treats a bad blob as
-- "no override" (resolveESIndexOverride returns "" on a parse failure), so
-- skipping it here preserves exactly that behaviour.

DO $$
DECLARE
    r        RECORD;
    parsed   jsonb;
    rebuilt  jsonb;
    elem     jsonb;
    idx      text;
BEGIN
    FOR r IN
        SELECT v.id, v.value
        FROM integration_config_values v
        JOIN integrations i ON i.id = v.integration_id
        WHERE i.type = 'ES'
          AND i.source = 'user'
          AND v.name = 'index_account_mapping'
          AND btrim(coalesce(v.value, '')) NOT IN ('', '[]')
    LOOP
        BEGIN
            parsed := r.value::jsonb;
        EXCEPTION WHEN others THEN
            CONTINUE;  -- malformed blob: the runtime ignores it too
        END;

        IF jsonb_typeof(parsed) <> 'array' THEN
            CONTINUE;
        END IF;

        rebuilt := '[]'::jsonb;
        FOR elem IN SELECT * FROM jsonb_array_elements(parsed)
        LOOP
            idx := btrim(coalesce(elem->>'trace_index', ''));
            IF jsonb_typeof(elem) = 'object'
               AND idx <> ''
               AND coalesce(elem->>'trace_schema', '') = ''
            THEN
                elem := elem || jsonb_build_object('trace_schema',
                    CASE
                        WHEN idx LIKE '%otel-v1-apm-span%' THEN 'data_prepper'
                        WHEN idx LIKE 'traces-apm%'        THEN 'elastic_apm'
                        WHEN idx LIKE '%.otel%'
                          OR idx LIKE 'traces-%'           THEN 'otel_native'
                        ELSE 'data_prepper'
                    END);
            END IF;
            rebuilt := rebuilt || jsonb_build_array(elem);
        END LOOP;

        IF rebuilt <> parsed THEN
            UPDATE integration_config_values
            SET value = rebuilt::text, updated_at = now()
            WHERE id = r.id;
        END IF;
    END LOOP;
END $$;
