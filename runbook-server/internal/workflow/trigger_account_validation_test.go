package workflow

import (
	"errors"
	"testing"

	"nudgebee/runbook/internal/model"
	"nudgebee/runbook/services/security"

	"github.com/stretchr/testify/assert"
)

// Saving refuses a trigger that listens to an account outside the caller's tenant
// or one the caller cannot read; the workflow's own account is never looked up.
func TestValidateTriggerAccounts(t *testing.T) {
	const (
		own     = "00000000-0000-0000-0000-00000000000a"
		peer    = "00000000-0000-0000-0000-00000000000b"
		hidden  = "00000000-0000-0000-0000-00000000000c"
		foreign = "00000000-0000-0000-0000-00000000000d"
		ghost   = "00000000-0000-0000-0000-00000000000e"
	)
	tenants := map[string]string{own: "tenant-1", peer: "tenant-1", hidden: "tenant-1", foreign: "tenant-2"}
	orig := tenantForAccount
	tenantForAccount = func(id string) (string, error) {
		if tenant, ok := tenants[id]; ok {
			return tenant, nil
		}
		return "", errors.New("account not found")
	}
	t.Cleanup(func() { tenantForAccount = orig })

	ctx := security.NewRequestContextForTenantAccountAdmin("tenant-1", "user-1", []string{own, peer})
	event := func(accounts ...any) []model.Trigger {
		return []model.Trigger{{Type: model.WorkflowTriggerEvent, Params: map[string]any{"event_type": "alert", "accounts": accounts}}}
	}
	for _, tc := range []struct {
		name     string
		triggers []model.Trigger
		wantErr  string
	}{
		{"own account only", event(), ""},
		{"own and accessible peer", event(own, peer), ""},
		{"account the user cannot read", event(own, hidden), hidden},
		{"account in another tenant", event(foreign), foreign},
		{"unknown account", event(ghost), ghost},
		{"uppercase account ID", event("00000000-0000-0000-0000-00000000000B"), "an account ID is expected"},
		{"cluster name instead of an account ID", []model.Trigger{{Type: model.WorkflowTriggerOptimization, Params: map[string]any{"clusters": []any{"k8s-dev"}}}}, "an account ID is expected"},
		{"optimization cluster outside access", []model.Trigger{{Type: model.WorkflowTriggerOptimization, Params: map[string]any{"clusters": []any{hidden}}}}, hidden},
		{"non-listening trigger ignores params", []model.Trigger{{Type: model.WorkflowTriggerSchedule, Params: map[string]any{"accounts": []any{foreign}}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTriggerAccounts(ctx, own, tc.triggers)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
