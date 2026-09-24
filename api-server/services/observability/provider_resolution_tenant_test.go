package observability

import (
	"context"
	"log/slog"
	"testing"

	"nudgebee/services/security"

	"github.com/stretchr/testify/assert"
)

// The case these cover, found by replaying cost-server's own request against a
// local api-server: the Prometheus shim authenticates with an action token and
// a role-only session, so api-server builds it a super-admin context with no
// tenant. Almost every read behind a metrics request is tenant-scoped, so
// naming a provider without a source — which is what the shim does since
// #39039 — failed for EVERY account, agent-connected ones included. Pinning the
// source used to hide that by skipping resolution altogether.

// tenantCarryingContext builds a context that carries a tenant without touching
// the database: NewSecurityContextForTenantAdmin reads the tenant's account
// list, and a unit test has no database for it.
func tenantCarryingContext(tenantId string) *security.RequestContext {
	return security.NewRequestContext(context.Background(),
		security.NewSecurityContextForAuditRelay(tenantId, ""), slog.Default(), nil, nil)
}

func TestScopeToAccountTenant_LeavesATenantCarryingContextAlone(t *testing.T) {
	ctx := tenantCarryingContext("tenant-of-the-caller")

	assert.Same(t, ctx, scopeToAccountTenant(ctx, "acc-1"),
		"a caller that already named a tenant must keep its own context, untouched")
}

func TestScopeToAccountTenant_LeavesAnAccountlessRequestAlone(t *testing.T) {
	// No account means nothing to resolve a tenant from. The entry points guard
	// the empty account themselves; this must not become a database read.
	ctx := newLogLabelCtx()

	assert.Same(t, ctx, scopeToAccountTenant(ctx, ""))
}

func TestScopeToAccountTenant_KeepsTheCallersContextWhenTheTenantCannotBeRead(t *testing.T) {
	// No database in a unit test, which is the same shape as an account with no
	// cloud_accounts row. The request must go on exactly as it does today rather
	// than fail here: this widens what works and must never narrow it.
	ctx := newLogLabelCtx()

	assert.Same(t, ctx, scopeToAccountTenant(ctx, "an-account-with-no-row"))
}

func TestScopeToAccountTenant_ToleratesAContextWithNoSecurityContext(t *testing.T) {
	assert.Nil(t, scopeToAccountTenant(nil, "acc-1"))
}
