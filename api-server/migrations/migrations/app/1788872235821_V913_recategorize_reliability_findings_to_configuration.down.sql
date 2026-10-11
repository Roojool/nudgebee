-- Reverse of the up: Configuration -> RightSizing for the same rows. Rows the
-- up migration deleted as collision losers are not restored (their fresher
-- Configuration twin carries the same finding).

-- Drop Configuration rows that would collide with a RightSizing twin on the
-- unique key once flipped back.
DELETE FROM recommendation loser
WHERE loser.category = 'Configuration'
  AND (
    loser.rule_name = 'azure_nsg_unassociated'
    OR (
      loser.rule_name = 'gcp_native_container_diagnosis'
      AND loser.recommendation ->> 'impact_category' IN ('RELIABILITY', 'PERFORMANCE')
    )
  )
  AND EXISTS (
    SELECT 1 FROM recommendation twin
    WHERE twin.cloud_account_id = loser.cloud_account_id
      AND twin.rule_name = loser.rule_name
      AND twin.category = 'RightSizing'
      AND COALESCE(twin.resource_id::text, '') = COALESCE(loser.resource_id::text, '')
      AND COALESCE(twin.account_object_id, '') = COALESCE(loser.account_object_id, '')
  );

UPDATE recommendation r
SET category = 'RightSizing', updated_at = NOW()
WHERE r.category = 'Configuration'
  AND (
    r.rule_name = 'azure_nsg_unassociated'
    OR (
      r.rule_name = 'gcp_native_container_diagnosis'
      AND r.recommendation ->> 'impact_category' IN ('RELIABILITY', 'PERFORMANCE')
    )
  );

UPDATE public.recommendation_rule
SET category = 'RightSizing'
WHERE rule_name = 'gcp_native_container_diagnosis'
  AND category = 'Configuration';

-- The catalog row for azure_nsg_unassociated did not exist before the up.
DELETE FROM public.recommendation_rule WHERE rule_name = 'azure_nsg_unassociated' AND cloud_provider IS NULL;
