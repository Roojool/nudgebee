package recommendation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"nudgebee/services/internal/database"
	"nudgebee/services/security"
)

// imageScanSettingsAttr is the cloud_account_attrs key the account settings
// drawer writes the per-account image-scan configuration under. Same shape as
// the other per-account recommendation settings there (abandoned_resource,
// certificate_expiry_recommendation): one row, one JSON value.
const imageScanSettingsAttr = "image_scan"

// imageScanAlwaysExcludedNamespaces are never scanned regardless of account
// configuration: kube-system is the cluster's own control plane and
// nudgebee-agent is where the scan Jobs themselves run.
var imageScanAlwaysExcludedNamespaces = []string{"kube-system", "nudgebee-agent"}

// imageScanSettings is the per-account image-scan configuration.
//
// Enabled is a pointer so "no attribute" and "explicitly enabled" stay
// distinguishable from "explicitly disabled" — an account that never opened the
// settings drawer scans everything, which is the behaviour before this setting
// existed.
type imageScanSettings struct {
	Enabled            *bool    `json:"enabled"`
	ExcludedNamespaces []string `json:"excluded_namespaces"`
}

// IsEnabled reports whether image scanning runs for the account. Absent
// configuration means enabled.
func (s imageScanSettings) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// namespaceExclusions returns the namespaces to skip: the always-excluded set
// plus whatever the account configured, trimmed, de-duplicated and sorted (sorted
// only so the value is stable in logs and tests).
func (s imageScanSettings) namespaceExclusions() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(imageScanAlwaysExcludedNamespaces)+len(s.ExcludedNamespaces))
	for _, ns := range append(append([]string{}, imageScanAlwaysExcludedNamespaces...), s.ExcludedNamespaces...) {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if _, dup := seen[ns]; dup {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// isNamespaceExcluded reports whether the account's configuration keeps this
// namespace out of image scanning.
func (s imageScanSettings) isNamespaceExcluded(namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	for _, ns := range s.namespaceExclusions() {
		if ns == namespace {
			return true
		}
	}
	return false
}

// getImageScanSettings reads the account's image-scan configuration. It fails
// open — a missing row, an unreadable one or malformed JSON yields the default
// (scan everything), because losing the setting must not silently stop a
// security scan.
func getImageScanSettings(ctx *security.RequestContext, dbms *database.DatabaseManager, accountId string) imageScanSettings {
	var raw string
	err := dbms.Db.Get(&raw,
		`SELECT value FROM cloud_account_attrs WHERE cloud_account_id = $1 AND name = $2`,
		accountId, imageScanSettingsAttr)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			ctx.GetLogger().Error("image_scanner: reading image scan settings, defaulting to enabled",
				"account_id", accountId, "error", err)
		}
		return imageScanSettings{}
	}
	if strings.TrimSpace(raw) == "" {
		return imageScanSettings{}
	}
	var settings imageScanSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		ctx.GetLogger().Error("image_scanner: image scan settings are not valid JSON, defaulting to enabled",
			"account_id", accountId, "error", err)
		return imageScanSettings{}
	}
	return settings
}
