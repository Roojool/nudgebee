CREATE INDEX IF NOT EXISTS recommendation_tenant ON public.recommendation USING btree (tenant_id);
CREATE INDEX IF NOT EXISTS "recommendation_resourceId" ON public.recommendation USING btree (resource_id);
CREATE INDEX IF NOT EXISTS recommendation_rulename ON public.recommendation USING btree (rule_name);
