package branding

import (
	"encoding/json"
	"sort"

	"nudgebee/services/audit"
	"nudgebee/services/common"
	"nudgebee/services/security"
)

// overrideKeyNames lists the field names in an override, sorted. Used in log
// lines so a rejected write says WHICH fields were involved without putting the
// values — which may be the very payload the validator refused — into the log.
func overrideKeyNames(o map[string]any) []string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// requireSuperAdmin gates every write.
//
// Rows are keyed by hostname and carry no tenant column, because branding must
// resolve before authentication — the signin page has no session. That makes an
// override deployment-level operator config, not tenant data: a tenant_admin of
// one partner could otherwise rewrite another partner's hostname, since there
// is no tenant scope to constrain them. Super-admin is the only role whose
// authority actually matches the blast radius.
//
// Consequence worth knowing: audit.CreateAudit deliberately skips super-admin
// operations, so these writes leave no audit row. That is precisely why the
// table carries previous_overrides — recovery does not depend on the audit log.
func requireSuperAdmin(ctx *security.RequestContext) error {
	if !ctx.GetSecurityContext().IsSuperAdmin() {
		// Logged rather than silently 4xx'd: branding is deployment-level
		// operator config with no tenant scope, so a refused write is worth a
		// record even though audit.LogChange cannot produce one for this path.
		ctx.GetLogger().Warn("branding: write refused, caller is not a full super admin",
			"user_id", ctx.GetSecurityContext().GetUserId(),
			"tenant_id", ctx.GetSecurityContext().GetTenantId())
		return common.ErrorUnauthorized("Only super admins can manage branding overrides")
	}
	return nil
}

// requireSuperAdminRead gates the two read actions. IsSuperAdmin() is the FULL
// role only, so checking it alone would reject super_admin_readonly — which
// actions.yaml explicitly grants on branding_list_overrides and
// branding_get_override, leaving the gateway to admit a caller the handler then
// refuses. Same split as tenant listing in api/actions_tenant.go.
func requireSuperAdminRead(ctx *security.RequestContext) error {
	sc := ctx.GetSecurityContext()
	if !sc.IsSuperAdmin() && !sc.IsSuperAdminReadonly() {
		ctx.GetLogger().Warn("branding: read refused, caller is not a super admin",
			"user_id", sc.GetUserId(), "tenant_id", sc.GetTenantId())
		return common.ErrorUnauthorized("Only super admins can view branding overrides")
	}
	return nil
}

// List returns every override row, including disabled ones, for the admin UI.
func List(ctx *security.RequestContext, _ ListRequest) (ListResponse, error) {
	if err := requireSuperAdminRead(ctx); err != nil {
		return ListResponse{}, err
	}
	d, err := db()
	if err != nil {
		ctx.GetLogger().Error("branding: database unavailable while listing overrides", "error", err)
		return ListResponse{}, err
	}
	rows, err := listRows(d, false)
	if err != nil {
		ctx.GetLogger().Error("branding: failed to list overrides", "error", err)
		return ListResponse{}, err
	}
	return ListResponse{Overrides: rows}, nil
}

// Get returns one host's override, or nil when none is stored.
//
// Pointer return so "no row" serializes as JSON null. A zero-value Row would
// reach the caller as a real object with an empty host and enabled:false, which
// reads as "an override exists and is switched off" — a different state with a
// different remedy.
func Get(ctx *security.RequestContext, req HostRequest) (*Row, error) {
	if err := requireSuperAdminRead(ctx); err != nil {
		return nil, err
	}
	if err := common.ValidateStruct(req); err != nil {
		ctx.GetLogger().Warn("branding: invalid get request", "error", err)
		return nil, err
	}
	host := NormalizeHost(req.Host)
	if host == "" {
		ctx.GetLogger().Warn("branding: get rejected, host normalized to empty", "raw_host", req.Host)
		return nil, common.ErrorBadRequest("host is required")
	}
	d, err := db()
	if err != nil {
		ctx.GetLogger().Error("branding: database unavailable while reading override", "error", err, "host", host)
		return nil, err
	}
	row, err := getRow(d, host)
	if err != nil {
		ctx.GetLogger().Error("branding: failed to read override", "error", err, "host", host)
		return nil, err
	}
	return row, nil
}

// Upsert stores a host's override after validating it against the allow-list.
func Upsert(ctx *security.RequestContext, req UpsertRequest) (StatusResponse, error) {
	if err := requireSuperAdmin(ctx); err != nil {
		return StatusResponse{}, err
	}
	if err := common.ValidateStruct(req); err != nil {
		ctx.GetLogger().Warn("branding: invalid upsert request", "error", err)
		return StatusResponse{}, err
	}
	host := NormalizeHost(req.Host)
	if host == "" {
		ctx.GetLogger().Warn("branding: upsert rejected, host normalized to empty", "raw_host", req.Host)
		return StatusResponse{}, common.ErrorBadRequest("host is required")
	}
	if err := ValidateOverrides(req.Overrides); err != nil {
		// Log the offending keys, never the values: an override can carry a
		// rejected payload (CSS/HTML breakout attempts are exactly what this
		// path refuses) and echoing it into the log would move the injection
		// attempt into whatever reads the logs.
		ctx.GetLogger().Warn("branding: upsert rejected by validation",
			"error", err, "host", host, "fields", overrideKeyNames(req.Overrides))
		return StatusResponse{}, common.ErrorBadRequest(err.Error())
	}
	encoded, err := json.Marshal(req.Overrides)
	if err != nil {
		ctx.GetLogger().Warn("branding: overrides not serializable", "error", err, "host", host)
		return StatusResponse{}, common.ErrorBadRequest("overrides is not serializable: " + err.Error())
	}
	if len(encoded) > MaxOverrideBytes {
		ctx.GetLogger().Warn("branding: upsert rejected, override too large",
			"host", host, "bytes", len(encoded), "limit", MaxOverrideBytes)
		return StatusResponse{}, common.ErrorBadRequest("overrides exceeds the maximum size")
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	d, err := db()
	if err != nil {
		ctx.GetLogger().Error("branding: database unavailable while writing override", "error", err, "host", host)
		return StatusResponse{}, err
	}
	prev, err := getRow(d, host)
	if err != nil {
		ctx.GetLogger().Error("branding: failed to read the prior override before writing",
			"error", err, "host", host)
		return StatusResponse{}, err
	}
	id, err := upsertRow(d, host, encoded, enabled, ctx.GetSecurityContext().GetUserId())
	if err != nil {
		ctx.GetLogger().Error("branding: failed to write override", "error", err, "host", host, "enabled", enabled)
		return StatusResponse{}, err
	}

	changeInput := audit.ChangeInput{
		EventCategory: audit.EventCategoryBranding,
		EventType:     audit.EventTypeBrandingOverrideUpsert,
		EventAction:   audit.EventActionUpdate,
		TargetID:      host,
		TableName:     "tenant_branding_override",
		NewData:       map[string]any{"host": host, "enabled": enabled, "overrides": req.Overrides},
	}
	if prev != nil {
		changeInput.OldData = map[string]any{"host": prev.Host, "enabled": prev.Enabled, "overrides": prev.Overrides}
	}
	audit.LogChange(ctx, changeInput)

	return StatusResponse{Status: "ok", Host: host, Id: id}, nil
}

// Restore swaps a host's override back to the value before its last write.
func Restore(ctx *security.RequestContext, req HostRequest) (StatusResponse, error) {
	if err := requireSuperAdmin(ctx); err != nil {
		return StatusResponse{}, err
	}
	if err := common.ValidateStruct(req); err != nil {
		ctx.GetLogger().Warn("branding: invalid restore request", "error", err)
		return StatusResponse{}, err
	}
	host := NormalizeHost(req.Host)
	if host == "" {
		ctx.GetLogger().Warn("branding: restore rejected, host normalized to empty", "raw_host", req.Host)
		return StatusResponse{}, common.ErrorBadRequest("host is required")
	}
	d, err := db()
	if err != nil {
		ctx.GetLogger().Error("branding: database unavailable while restoring override", "error", err, "host", host)
		return StatusResponse{}, err
	}
	n, err := restoreRow(d, host, ctx.GetSecurityContext().GetUserId())
	if err != nil {
		ctx.GetLogger().Error("branding: failed to restore override", "error", err, "host", host)
		return StatusResponse{}, err
	}
	if n == 0 {
		// Either no row at all, or a row that has never been overwritten. Both
		// are caller-visible states, but this is the one place an operator can
		// reach for undo and get nothing, so leave a trace.
		ctx.GetLogger().Warn("branding: restore found no previous version", "host", host)
		return StatusResponse{}, common.ErrorBadRequest("no previous version stored for this host")
	}
	audit.LogChange(ctx, audit.ChangeInput{
		EventCategory: audit.EventCategoryBranding,
		EventType:     audit.EventTypeBrandingOverrideRestore,
		EventAction:   audit.EventActionUpdate,
		TargetID:      host,
		TableName:     "tenant_branding_override",
		NewData:       map[string]any{"host": host, "restored": true},
	})
	return StatusResponse{Status: "ok", Host: host, Count: int(n)}, nil
}

// Delete removes a host's override; the host falls back to the house brand.
func Delete(ctx *security.RequestContext, req HostRequest) (StatusResponse, error) {
	if err := requireSuperAdmin(ctx); err != nil {
		return StatusResponse{}, err
	}
	if err := common.ValidateStruct(req); err != nil {
		ctx.GetLogger().Warn("branding: invalid delete request", "error", err)
		return StatusResponse{}, err
	}
	host := NormalizeHost(req.Host)
	if host == "" {
		ctx.GetLogger().Warn("branding: delete rejected, host normalized to empty", "raw_host", req.Host)
		return StatusResponse{}, common.ErrorBadRequest("host is required")
	}
	d, err := db()
	if err != nil {
		ctx.GetLogger().Error("branding: database unavailable while deleting override", "error", err, "host", host)
		return StatusResponse{}, err
	}
	prev, err := getRow(d, host)
	if err != nil {
		ctx.GetLogger().Error("branding: failed to read the override before deleting", "error", err, "host", host)
		return StatusResponse{}, err
	}
	n, err := deleteRow(d, host)
	if err != nil {
		ctx.GetLogger().Error("branding: failed to delete override", "error", err, "host", host)
		return StatusResponse{}, err
	}
	if n > 0 {
		changeInput := audit.ChangeInput{
			EventCategory: audit.EventCategoryBranding,
			EventType:     audit.EventTypeBrandingOverrideDelete,
			EventAction:   audit.EventActionDelete,
			TargetID:      host,
			TableName:     "tenant_branding_override",
		}
		if prev != nil {
			changeInput.OldData = map[string]any{"host": prev.Host, "enabled": prev.Enabled, "overrides": prev.Overrides}
		}
		audit.LogChange(ctx, changeInput)
	}
	return StatusResponse{Status: "ok", Host: host, Count: int(n)}, nil
}
