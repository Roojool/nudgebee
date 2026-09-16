package azure

import (
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"

	"nudgebee/collector/cloud/providers"
)

// UpdateAzureMetricAlertThreshold changes the static threshold, window size and/or the
// enabled switch of an existing Azure metric alert (Get, patch, CreateOrUpdate), so
// scopes, actions and evaluation frequency stay as they are. It returns the values it
// replaced so a revert can restore them.
//
// ruleRef is either a full ARM resource id
// (/subscriptions/<sub>/resourceGroups/<rg>/providers/microsoft.insights/metricAlerts/<name>)
// or — as threshold suggestions store it — just the alert name, in which case the alert
// is looked up across the account's subscriptions and must be unique.
func UpdateAzureMetricAlertThreshold(ctx providers.CloudProviderContext, account providers.Account, ruleRef string, change providers.AlertRuleChange) (map[string]any, error) {
	if ruleRef == "" {
		return nil, fmt.Errorf("alert reference (resource id or name) is required")
	}

	cred, session, err := getAzureCredsForAccount(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("failed to create azure credential: %w", err)
	}

	// Full ARM resource id → patch directly.
	if strings.Contains(strings.ToLower(ruleRef), "/subscriptions/") {
		subID, _ := extractSubscriptionID(ruleRef)
		if subID == "" {
			subID = strings.TrimSpace(strings.Split(session.SubscriptionID, ",")[0])
		}
		rg, err := extractResourceGroup(ruleRef)
		if err != nil {
			return nil, fmt.Errorf("failed to extract resource group from resource id: %w", err)
		}
		name := azureResourceNameFromID(ruleRef)
		if name == "" {
			return nil, fmt.Errorf("could not extract alert name from resource id %q", ruleRef)
		}
		client, err := armmonitor.NewMetricAlertsClient(subID, cred, getAzureMetricAlertsOpts(ctx))
		if err != nil {
			return nil, fmt.Errorf("failed to create metric alerts client: %w", err)
		}
		return patchAndUpdateAzureAlert(ctx, client, rg, name, change)
	}

	// Otherwise ruleRef is the alert name → find it across the account's subscriptions.
	type match struct {
		client *armmonitor.MetricAlertsClient
		rg     string
		id     string
	}
	name := ruleRef
	var matches []match
	for _, subID := range strings.Split(session.SubscriptionID, ",") {
		subID = strings.TrimSpace(subID)
		if subID == "" {
			continue
		}
		client, err := armmonitor.NewMetricAlertsClient(subID, cred, getAzureMetricAlertsOpts(ctx))
		if err != nil {
			ctx.GetLogger().Warn("azure: failed to create metric alerts client", "subscriptionId", subID, "error", err)
			continue
		}
		pager := client.NewListBySubscriptionPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx.GetContext())
			if err != nil {
				ctx.GetLogger().Warn("azure: failed to list metric alerts", "subscriptionId", subID, "error", err)
				break
			}
			for _, alert := range page.Value {
				if alert.Name == nil || !strings.EqualFold(*alert.Name, name) || alert.ID == nil {
					continue
				}
				rg, _ := extractResourceGroup(*alert.ID)
				if rg == "" {
					return nil, fmt.Errorf("could not resolve resource group for azure alert %q", name)
				}
				matches = append(matches, match{client: client, rg: rg, id: *alert.ID})
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("azure metric alert %q not found in any subscription", name)
	case 1:
		return patchAndUpdateAzureAlert(ctx, matches[0].client, matches[0].rg, name, change)
	default:
		// Changing whichever came first would edit an alert nobody asked about.
		return nil, fmt.Errorf("%w: %d Azure metric alerts are named %q; the alert's resource id is needed", providers.ErrAlertRuleChangeUnsupported, len(matches), name)
	}
}

// patchAndUpdateAzureAlert fetches the metric alert, applies the change in place, and
// writes it back. Get-then-CreateOrUpdate keeps every other field intact.
func patchAndUpdateAzureAlert(ctx providers.CloudProviderContext, client *armmonitor.MetricAlertsClient, rg, name string, change providers.AlertRuleChange) (map[string]any, error) {
	resp, err := client.Get(ctx.GetContext(), rg, name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get metric alert %q: %w", name, err)
	}
	rule := resp.MetricAlertResource
	if rule.Properties == nil {
		return nil, fmt.Errorf("metric alert %q has no properties", name)
	}

	previous, err := applyAzureAlertChange(rule.Properties, change)
	if err != nil {
		return nil, fmt.Errorf("metric alert %q: %w", name, err)
	}

	if _, err := client.CreateOrUpdate(ctx.GetContext(), rg, name, rule, nil); err != nil {
		return nil, fmt.Errorf("failed to update metric alert %q: %w", name, err)
	}

	ctx.GetLogger().Info("updated Azure metric alert", "alert", name)
	return previous, nil
}

// azureWindowSizes are the window sizes Azure metric alerts accept, in minutes.
var azureWindowSizes = []struct {
	minutes int
	iso     string
}{
	{1, "PT1M"}, {5, "PT5M"}, {15, "PT15M"}, {30, "PT30M"},
	{60, "PT1H"}, {360, "PT6H"}, {720, "PT12H"}, {1440, "P1D"},
}

// azureWindowSize returns the smallest accepted window of at least minutes.
func azureWindowSize(minutes int) (string, error) {
	for _, w := range azureWindowSizes {
		if w.minutes >= minutes {
			return w.iso, nil
		}
	}
	return "", fmt.Errorf("%w: Azure metric alert windows are at most one day", providers.ErrAlertRuleChangeUnsupported)
}

// applyAzureAlertChange mutates the alert properties and returns the replaced values.
// Pure, so the guards are testable without Azure.
func applyAzureAlertChange(props *armmonitor.MetricAlertProperties, change providers.AlertRuleChange) (map[string]any, error) {
	threshold := change.Threshold
	enabled := change.Enabled
	window := ""
	if change.DurationMinutes > 0 {
		w, err := azureWindowSize(change.DurationMinutes)
		if err != nil {
			return nil, err
		}
		window = w
	}
	if r := change.Restore; len(r) > 0 {
		if v, ok := providers.RestoreNumber(r, "threshold"); ok {
			threshold = &v
		}
		if v, ok := r["window_size"].(string); ok && v != "" {
			window = v
		}
		if v, ok := r["enabled"].(bool); ok {
			enabled = &v
		}
	}

	criteria := azureStaticCriteria(props.Criteria)
	previous := map[string]any{"enabled": props.Enabled == nil || *props.Enabled}
	if props.WindowSize != nil {
		previous["window_size"] = *props.WindowSize
	}
	if len(criteria) > 0 && criteria[0].Threshold != nil {
		previous["threshold"] = *criteria[0].Threshold
	}

	changed := false
	if threshold != nil {
		if len(criteria) == 0 {
			return nil, fmt.Errorf("%w: alert has no static threshold criterion", providers.ErrAlertRuleChangeUnsupported)
		}
		for _, c := range criteria[1:] {
			if c.Threshold == nil || criteria[0].Threshold == nil || *c.Threshold != *criteria[0].Threshold {
				return nil, fmt.Errorf("%w: alert criteria use different thresholds", providers.ErrAlertRuleChangeUnsupported)
			}
		}
		for _, c := range criteria {
			c.Threshold = to.Ptr(*threshold)
		}
		changed = true
	}
	if window != "" {
		props.WindowSize = to.Ptr(window)
		changed = true
	}
	if enabled != nil {
		props.Enabled = to.Ptr(*enabled)
		changed = true
	}
	if !changed {
		return nil, fmt.Errorf("%w: nothing to change", providers.ErrAlertRuleChangeUnsupported)
	}
	return previous, nil
}

// azureStaticCriteria returns the static-threshold criteria of either criteria shape.
func azureStaticCriteria(criteria armmonitor.MetricAlertCriteriaClassification) []*armmonitor.MetricCriteria {
	var out []*armmonitor.MetricCriteria
	switch c := criteria.(type) {
	case *armmonitor.MetricAlertSingleResourceMultipleMetricCriteria:
		for _, m := range c.AllOf {
			if m != nil {
				out = append(out, m)
			}
		}
	case *armmonitor.MetricAlertMultipleResourceMultipleMetricCriteria:
		for _, mc := range c.AllOf {
			if m, ok := mc.(*armmonitor.MetricCriteria); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// azureResourceNameFromID returns the final path segment of an ARM resource ID.
func azureResourceNameFromID(resourceID string) string {
	trimmed := strings.TrimRight(resourceID, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 || idx == len(trimmed)-1 {
		return ""
	}
	return trimmed[idx+1:]
}
