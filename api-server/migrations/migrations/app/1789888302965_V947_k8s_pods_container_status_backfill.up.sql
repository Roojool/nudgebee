-- Backfill k8s_pods.container_status for pods that already exist.
--
-- Separate from V946 (which adds the column) on purpose: Atlas runs one file
-- per transaction, and ADD COLUMN's ACCESS EXCLUSIVE lock would otherwise be
-- held for this whole statement, blocking every reader and writer of k8s_pods.
-- On its own this UPDATE takes row locks: a concurrent discovery upsert to one
-- of the affected pods waits, everything else is unaffected.
--
-- Cost: one pass over the account's live pods reading meta (~10s on dev's 94k;
-- prod has 291k live rows on a 31GB table). Only pods with a not-ready
-- container are written -- ~2k on dev.
--
-- Deleted pods are deliberately left NULL. The pods table computes their reason
-- from meta for the rows on screen, so history still reads correctly, while
-- backfilling them rewrites far more of the table than it is worth (measured on
-- dev: 185,904 rows in 88s, most of them pods that died months ago). Counts and
-- filters are about live pods.
--
-- Idempotent: only rows still NULL are touched, so a re-run after the collector
-- has started writing the column is a no-op.
-- Backfill: same precedence the collector now writes -- init containers before
-- app containers, waiting before terminated, exit code 0 skipped.
UPDATE k8s_pods ksp
   SET container_status = (
     SELECT cs.reason
       FROM (SELECT ksp.meta->'status_info' AS si) src
       CROSS JOIN LATERAL (
         SELECT COALESCE(c.value->'state'->'waiting'->>'reason',
                         c.value->'state'->'terminated'->>'reason') AS reason
           FROM (
             SELECT value, ord, 0 AS grp
               FROM jsonb_array_elements(CASE WHEN jsonb_typeof(src.si->'initContainerStatuses') = 'array'
                                              THEN src.si->'initContainerStatuses' ELSE '[]'::jsonb END)
                    WITH ORDINALITY AS t(value, ord)
             UNION ALL
             SELECT value, ord, 1 AS grp
               FROM jsonb_array_elements(CASE WHEN jsonb_typeof(src.si->'containerStatuses') = 'array'
                                              THEN src.si->'containerStatuses' ELSE '[]'::jsonb END)
                    WITH ORDINALITY AS t(value, ord)
           ) c
          WHERE c.value->'state'->'waiting'->>'reason' IS NOT NULL
             OR (c.value->'state'->'terminated'->>'reason' IS NOT NULL
                 AND COALESCE(c.value->'state'->'terminated'->>'exitCode', '') <> '0')
          ORDER BY c.grp, (c.value->'state'->'waiting'->>'reason') IS NULL, c.ord
          LIMIT 1
       ) cs
   )
 WHERE ksp.is_active
   AND ksp.container_status IS NULL
   AND (ksp.meta->'status_info' @> '{"containerStatuses":[{"ready":false}]}'
     OR ksp.meta->'status_info' @> '{"initContainerStatuses":[{"ready":false}]}');
