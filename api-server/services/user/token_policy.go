package user

import (
	"fmt"
	"regexp"

	"nudgebee/services/common"
)

// A platform key opens the app API, a gateway key the AI Gateway, an all key both.
const (
	KindPlatform = "platform"
	KindGateway  = "gateway"
	KindAll      = "all"
)

// carriesGatewayTraffic reports whether quotas mean anything on a key. The gateway's own
// lookup filters on the same two kinds.
func carriesGatewayTraffic(kind string) bool {
	return kind == KindGateway || kind == KindAll
}

// TokenLimit is one quota on a key, and the shape tenant/user ceilings are read back in.
type TokenLimit struct {
	Metric string  `json:"metric" mapstructure:"metric"` // requests | tokens | cost
	Period string  `json:"period" mapstructure:"period"` // minute | hour | day | month
	Value  float64 `json:"value" mapstructure:"value"`
	Model  string  `json:"model" mapstructure:"model"` // "" = any model
}

var validMetrics = map[string]bool{"requests": true, "tokens": true, "cost": true}
var validPeriods = map[string]bool{"minute": true, "hour": true, "day": true, "month": true}

// An empty kind mints an all key: older clients omit the field, and a PAT has always
// opened both doors, so they must keep the access they have.
func normalizeKind(kind string) (string, error) {
	switch kind {
	case "":
		return KindAll, nil
	case KindPlatform, KindGateway, KindAll:
		return kind, nil
	default:
		return "", common.ErrorBadRequest(fmt.Sprintf("unknown key kind %q", kind))
	}
}

// Quotas only mean something on metered LLM traffic, so limits on a platform key are an
// error rather than a silently dropped field (D8).
func validateLimitsForKind(kind string, limits []TokenLimit) error {
	if len(limits) == 0 {
		return nil
	}
	if !carriesGatewayTraffic(kind) {
		return common.ErrorBadRequest("limits can only be set on a key that reaches the AI Gateway (kind=gateway or kind=all); a platform key carries an expiry only")
	}
	// Rows sharing (metric, period, model) share one limiter counter, so a duplicate
	// would count every request twice.
	seen := map[[3]string]bool{}
	for _, l := range limits {
		k := [3]string{l.Metric, l.Period, l.Model}
		if seen[k] {
			return common.ErrorBadRequest(fmt.Sprintf("duplicate limit: %s per %s for model %q appears more than once", l.Metric, l.Period, l.Model))
		}
		seen[k] = true
		if !validMetrics[l.Metric] {
			return common.ErrorBadRequest(fmt.Sprintf("unknown limit metric %q (expected requests, tokens or cost)", l.Metric))
		}
		if !validPeriods[l.Period] {
			return common.ErrorBadRequest(fmt.Sprintf("unknown limit period %q (expected minute, hour, day or month)", l.Period))
		}
		if l.Value <= 0 {
			return common.ErrorBadRequest("limit value must be greater than zero")
		}
	}
	return nil
}

// scopePattern matches a permission-catalog key (app/src/lib/permissionCatalog.ts
// permissionKey). Whether the module exists is the gateway's call: an unknown module
// grants nothing there, so it cannot widen a key.
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*:(Read|Write|Execute)$`)

// maxScopes bounds the list well above the catalog's size; it only stops a runaway payload.
const maxScopes = 500

// normalizeScopes validates a key's scopes and returns them deduplicated. Omitted (nil)
// means the owner's full access. An explicit empty list is rejected rather than read as
// full access: a caller sending [] most plausibly means "nothing", and widening that
// silently is the wrong way to fail. Only a platform key may carry scopes: the AI Gateway
// never reads them, so on a gateway or all key they would read as protection that is not
// applied (the same reasoning as limits on a platform key, D8).
func normalizeScopes(kind string, scopes []string) ([]string, error) {
	if scopes == nil {
		return nil, nil
	}
	if len(scopes) == 0 {
		return nil, common.ErrorBadRequest("scopes is empty, which would allow nothing; omit scopes for a full-access key")
	}
	if kind != KindPlatform {
		return nil, common.ErrorBadRequest("scopes can only be set on a platform key (kind=platform); the AI Gateway does not enforce them")
	}
	if len(scopes) > maxScopes {
		return nil, common.ErrorBadRequest(fmt.Sprintf("too many scopes (max %d)", maxScopes))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if !scopePattern.MatchString(s) {
			return nil, common.ErrorBadRequest(fmt.Sprintf("invalid scope %q (expected <module>:Read, <module>:Write or <module>:Execute)", s))
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// validateDeleteRequest accepts either identifier, preferring the id.
func validateDeleteRequest(r UserTokenDeleteRequest) error {
	if r.Id == "" && r.Name == "" {
		return common.ErrorBadRequest("id or name is required")
	}
	return nil
}

// validateLimitsAgainstCeilings rejects any owner limit above the tightest configured
// ceiling for its metric+period. Shared by create and update so the two cannot drift.
func validateLimitsAgainstCeilings(ceilings []TokenLimit, limits []TokenLimit) error {
	for _, l := range limits {
		if ceiling, ok := tightestCeiling(ceilings, l.Metric, l.Period); ok && l.Value > ceiling {
			return common.ErrorBadRequest(
				fmt.Sprintf("%s limit per %s exceeds the configured ceiling of %g", l.Metric, l.Period, ceiling))
		}
	}
	return nil
}

// resolveDeleteID picks the one key id a name-based delete meant. A name may exist once
// per kind, so more than one match is ambiguous and must not be guessed at.
func resolveDeleteID(matchingIDs []string) (string, error) {
	switch len(matchingIDs) {
	case 0:
		return "", common.ErrorBadRequest("token not found")
	case 1:
		return matchingIDs[0], nil
	default:
		return "", common.ErrorBadRequest("name is ambiguous: multiple keys share this name across key kinds; delete by id instead")
	}
}

// tightestCeiling returns the smallest configured ceiling for a metric+period, if any.
func tightestCeiling(ceilings []TokenLimit, metric, period string) (float64, bool) {
	found := false
	var min float64
	for _, c := range ceilings {
		if c.Metric != metric || c.Period != period {
			continue
		}
		if !found || c.Value < min {
			min, found = c.Value, true
		}
	}
	return min, found
}
