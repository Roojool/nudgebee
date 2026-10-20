package llm

import (
	"context"
	"database/sql"
	"fmt"
)

// AttachTenantDefaultLLMProviderSQL links the tenant's default LLM provider
// (the LLM integration whose default_llm_provider config value is 'true') to a
// newly created cloud account and flags it
// as that account's default, so the account resolves to it without anyone
// editing the provider. It does nothing when the tenant has no enabled default,
// or — defensively — more than one.
//
// $1 = tenant id, $2 = cloud account id.
//
// cloud-collector's AWS organization auto-registration carries a copy of this
// statement (it is a separate Go module); keep the two in sync.
const AttachTenantDefaultLLMProviderSQL = `
	INSERT INTO integrations_cloud_accounts (
		integration_id, cloud_account_id, tenant_id, link_role, default_llm_provider
	)
	SELECT i.id, $2, $1, 'own', true
	FROM integrations i
	JOIN integration_config_values icv
	  ON icv.integration_id = i.id AND icv.name = 'default_llm_provider' AND icv.value = 'true'
	WHERE i.tenant_id = $1
	  AND i.type = 'llm'
	  AND i.status = 'enabled'
	  AND (
		SELECT count(*) FROM integrations d
		JOIN integration_config_values dv
		  ON dv.integration_id = d.id AND dv.name = 'default_llm_provider' AND dv.value = 'true'
		WHERE d.tenant_id = $1 AND d.type = 'llm' AND d.status = 'enabled'
	  ) = 1
	ON CONFLICT (integration_id, cloud_account_id, tenant_id, link_role) DO NOTHING`

type execContexter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// AttachTenantDefaultLLMProvider runs AttachTenantDefaultLLMProviderSQL for a
// freshly created account. Callers treat a failure as non-fatal: the account
// exists either way, and an admin can still link a provider by hand.
func AttachTenantDefaultLLMProvider(ctx context.Context, db execContexter, tenantId, accountId string) error {
	if _, err := db.ExecContext(ctx, AttachTenantDefaultLLMProviderSQL, tenantId, accountId); err != nil {
		return fmt.Errorf("llm: failed to attach the tenant default LLM provider to account %s: %w", accountId, err)
	}
	return nil
}
