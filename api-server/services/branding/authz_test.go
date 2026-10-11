package branding

import (
	"context"
	"testing"

	"nudgebee/services/security"
)

func ctxWithRole(role string) *security.RequestContext {
	sc := security.NewSecurityContextForAuditRelay("", "")
	sc.AddRole(role)
	return security.NewRequestContext(context.TODO(), sc, nil, nil, nil)
}

// actions.yaml grants super_admin_readonly on branding_list_overrides and
// branding_get_override, so the gateway admits those callers; a handler that
// only checked IsSuperAdmin() (the FULL role) would then refuse them, and the
// admin screen would be unusable for read-only super admins.
func TestReadGateAcceptsReadonlySuperAdmin(t *testing.T) {
	if err := requireSuperAdminRead(ctxWithRole(security.AUTH_SUPER_ADMIN_READONLY_ROLE)); err != nil {
		t.Fatalf("readonly super admin must pass the read gate, got %v", err)
	}
	if err := requireSuperAdminRead(ctxWithRole(security.AUTH_SUPER_ADMIN_FULL_ROLE)); err != nil {
		t.Fatalf("full super admin must pass the read gate, got %v", err)
	}
}

// Writes stay full-super-admin only, matching actions.yaml.
func TestWriteGateRejectsReadonlySuperAdmin(t *testing.T) {
	if err := requireSuperAdmin(ctxWithRole(security.AUTH_SUPER_ADMIN_READONLY_ROLE)); err == nil {
		t.Fatal("readonly super admin must NOT pass the write gate")
	}
	if err := requireSuperAdmin(ctxWithRole(security.AUTH_SUPER_ADMIN_FULL_ROLE)); err != nil {
		t.Fatalf("full super admin must pass the write gate, got %v", err)
	}
}
