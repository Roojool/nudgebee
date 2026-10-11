-- Move two zero-savings, non-cost rules out of category='RightSizing' (the
-- Optimise Cost tab) into category='Configuration':
--
--   azure_nsg_unassociated         hygiene: an NSG attached to nothing costs
--                                  nothing; the producer now emits Configuration
--   gcp_native_container_diagnosis GCP's own impact category is RELIABILITY /
--                                  PERFORMANCE; the impact→category mapper
--                                  already yields Configuration for those, but
--                                  rows written before the mapper kept the
--                                  RightSizing fallback
--
-- In-place UPDATE (not archive+reinsert) on purpose: it preserves id,
-- created_at, is_dismissed, notes and resolution links, so the producer's
-- ON CONFLICT (cloud_account_id, rule_name, resource_id, category,
-- account_object_id) lands on this row instead of minting a new one.
--
-- No status filter: dismissed / closed / archived rows follow their category
-- too, otherwise the next scan inserts a fresh Configuration row alongside a
-- dismissed RightSizing twin and the dismissal is silently lost.
--
-- Expected lock window: a handful of rows per tenant (11 on dev); the
-- rule_name filter keeps both statements to an index-range scan.

-- Step 1: among the rows Step 2 will flip, drop any that already have a
-- Configuration twin on the unique key (status is not part of the key, so
-- archiving would not dodge the collision). The twin is the fresher
-- producer-written row, so it wins. Scoped to the flip candidates only.
DELETE FROM recommendation loser
WHERE loser.category = 'RightSizing'
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
      AND twin.category = 'Configuration'
      AND COALESCE(twin.resource_id::text, '') = COALESCE(loser.resource_id::text, '')
      AND COALESCE(twin.account_object_id, '') = COALESCE(loser.account_object_id, '')
  );

-- Step 2: recategorize the rest in place.
UPDATE recommendation r
SET category = 'Configuration', updated_at = NOW()
WHERE r.category = 'RightSizing'
  AND (
    r.rule_name = 'azure_nsg_unassociated'
    OR (
      r.rule_name = 'gcp_native_container_diagnosis'
      AND r.recommendation ->> 'impact_category' IN ('RELIABILITY', 'PERFORMANCE')
    )
  );

-- Step 3: keep the rule catalog (recommendation_rule, seeded from app
-- data.tsx in V666 and renamed in V683) in step with the rows above, so
-- anything that labels a finding by its rule reads the same category the row
-- now carries. cloud_provider stays NULL for the NSG row: it is not a
-- provider-merged rule.
UPDATE public.recommendation_rule
SET category = 'Configuration'
WHERE rule_name = 'gcp_native_container_diagnosis'
  AND category = 'RightSizing';

INSERT INTO public.recommendation_rule
  (rule_name, category, title, description, service_name, recommendations, mitigations, compliances, "references")
VALUES
  ('azure_nsg_unassociated', 'Configuration', 'NSG Not Associated With Any Subnet Or NIC',
   'A Network Security Group is not attached to any subnet or network interface. It filters no traffic, so it provides no protection and only adds clutter to the network configuration.',
   'AzureNSG',
   '["Attach the NSG to the subnets or network interfaces it was created for, or delete it if it is no longer needed."]'::jsonb,
   '["Associate the NSG with a subnet or network interface, or delete the unused NSG."]'::jsonb,
   '[]'::jsonb,
   '["https://learn.microsoft.com/en-us/azure/virtual-network/network-security-groups-overview"]'::jsonb)
ON CONFLICT (rule_name, COALESCE(cloud_provider, '')) DO UPDATE SET category = 'Configuration';
