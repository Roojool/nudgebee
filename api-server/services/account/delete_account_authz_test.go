package account

import (
	"context"
	"log/slog"
	"testing"

	"nudgebee/services/internal/testenv"
	"nudgebee/services/security"

	"github.com/stretchr/testify/require"
)

// Callers that are neither a super admin nor an admin of their own tenant are
// refused before the database is touched, so no database is needed here.
func TestDeleteAccountRefusesCallersWithoutDeleteAccess(t *testing.T) {
	withRole := func(tenantId, role string) *security.SecurityContext {
		sc := security.NewSecurityContextForAuditRelay(tenantId, testenv.FakeUserID)
		if role != "" {
			sc.AddRole(role)
		}
		return sc
	}

	cases := map[string]*security.SecurityContext{
		"user without an admin role":    withRole(testenv.FakeTenantID, ""),
		"read-only tenant admin":        withRole(testenv.FakeTenantID, security.AUTH_TENANT_READ_ADMIN_ROLE),
		"read-only super admin":         withRole("", security.AUTH_SUPER_ADMIN_READONLY_ROLE),
		"tenant admin without a tenant": withRole("", security.AUTH_TENANT_ADMIN_ROLE),
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := security.NewRequestContext(context.Background(), sc, slog.Default(), nil, nil)
			_, err := DeleteAccount(ctx, AccountDeleteRequest{Id: testenv.FakeAccountID})
			require.EqualError(t, err, "Unauthorized")
		})
	}
}
