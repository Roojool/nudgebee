package account

import (
	"fmt"
	"strconv"
	"strings"

	"nudgebee/collector/cloud/providers"
	"nudgebee/collector/cloud/providers/aws"
	"nudgebee/collector/cloud/providers/azure"
	"nudgebee/collector/cloud/providers/gcloud"
	"nudgebee/collector/cloud/security"
)

// UpdateAlertRuleRequest mirrors the payload sent by the api-server CloudAlertRuleSource.
type UpdateAlertRuleRequest struct {
	AccountId      string                 `json:"account_id" validate:"required"`
	ExternalRuleId string                 `json:"external_rule_id"`
	Name           string                 `json:"name"`
	AlertType      string                 `json:"alert_type"`
	Query          string                 `json:"query"`
	Severity       string                 `json:"severity"`
	Duration       string                 `json:"duration"`
	Enabled        bool                   `json:"enabled"`
	ProviderConfig map[string]interface{} `json:"provider_config"`
	// SetEnabled turns the rule on or off. Enabled above is not used for that:
	// older api-servers send enabled=true on every threshold edit, which would
	// silently re-enable a disabled rule.
	SetEnabled *bool `json:"set_enabled,omitempty"`
	// Restore carries the "previous" values of an earlier update, for a revert.
	Restore map[string]any `json:"restore,omitempty"`
}

// UpdateAlertRuleResponse is returned to the api-server.
type UpdateAlertRuleResponse struct {
	ExternalRuleId string `json:"external_rule_id"`
	Status         string `json:"status"`
	// Previous holds the provider values this update replaced (pass them back
	// as Restore to undo it).
	Previous map[string]any `json:"previous,omitempty"`
}

// alertRuleChangeFromRequest collects what the request asks to change. A request
// with only a duration is valid: duration-only suggestions send no threshold.
func alertRuleChangeFromRequest(req UpdateAlertRuleRequest) providers.AlertRuleChange {
	change := providers.AlertRuleChange{
		DurationMinutes: intFromConfig(req.ProviderConfig, "duration_minutes"),
		Enabled:         req.SetEnabled,
		Restore:         req.Restore,
	}
	if threshold, ok := floatFromConfig(req.ProviderConfig, "threshold"); ok {
		change.Threshold = &threshold
	}
	return change
}

// UpdateAlertRule applies a threshold / window / enabled change to a cloud provider's
// alert rule and returns the values it replaced.
func UpdateAlertRule(ctx *security.RequestContext, req UpdateAlertRuleRequest) (UpdateAlertRuleResponse, error) {
	change := alertRuleChangeFromRequest(req)
	if err := change.Validate(); err != nil {
		return UpdateAlertRuleResponse{}, err
	}

	acct, providerName, err := getAccount(ctx, req.AccountId)
	if err != nil {
		return UpdateAlertRuleResponse{}, fmt.Errorf("failed to get account: %w", err)
	}

	switch strings.ToLower(providerName) {
	case "aws":
		arn := req.ExternalRuleId
		if arn == "" {
			arn = stringFromConfig(req.ProviderConfig, "alarm_arn")
		}
		region, alarmName := parseCloudWatchAlarmARN(arn)
		if alarmName == "" {
			// Fall back to the rule name when no ARN is available.
			alarmName = req.Name
		}
		if region == "" {
			region = stringFromConfig(req.ProviderConfig, "region")
		}
		if region == "" || alarmName == "" {
			return UpdateAlertRuleResponse{}, fmt.Errorf("could not resolve CloudWatch alarm region/name (arn=%q, name=%q)", arn, req.Name)
		}

		previous, err := aws.UpdateCloudWatchAlarmThreshold(ctx.GetContext(), acct, alarmName, region, change)
		if err != nil {
			return UpdateAlertRuleResponse{}, err
		}
		return UpdateAlertRuleResponse{ExternalRuleId: arn, Status: "updated", Previous: previous}, nil

	case "gcp":
		policyName := req.ExternalRuleId
		if policyName == "" {
			return UpdateAlertRuleResponse{}, fmt.Errorf("external_rule_id (alert policy name) is required for GCP")
		}
		cloudCtx := providers.NewCloudProviderContextWithLogger(ctx.GetContext(), ctx.GetLogger())
		previous, err := gcloud.UpdateGCPAlertPolicyThreshold(cloudCtx, acct, policyName, change)
		if err != nil {
			return UpdateAlertRuleResponse{}, err
		}
		return UpdateAlertRuleResponse{ExternalRuleId: policyName, Status: "updated", Previous: previous}, nil

	case "azure":
		resourceID := req.ExternalRuleId
		if resourceID == "" {
			return UpdateAlertRuleResponse{}, fmt.Errorf("external_rule_id (alert resource id or name) is required for Azure")
		}
		cloudCtx := providers.NewCloudProviderContextWithLogger(ctx.GetContext(), ctx.GetLogger())
		previous, err := azure.UpdateAzureMetricAlertThreshold(cloudCtx, acct, resourceID, change)
		if err != nil {
			return UpdateAlertRuleResponse{}, err
		}
		return UpdateAlertRuleResponse{ExternalRuleId: resourceID, Status: "updated", Previous: previous}, nil

	default:
		return UpdateAlertRuleResponse{}, fmt.Errorf("%w: alert rule update not yet supported for provider %q", providers.ErrAlertRuleChangeUnsupported, providerName)
	}
}

// parseCloudWatchAlarmARN extracts (region, alarmName) from a CloudWatch alarm ARN of the form
// arn:aws:cloudwatch:<region>:<account>:alarm:<name>. Returns empty strings if it is not an ARN.
func parseCloudWatchAlarmARN(arn string) (string, string) {
	if !strings.HasPrefix(arn, "arn:") {
		return "", ""
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 7 || parts[5] != "alarm" {
		return "", ""
	}
	region := parts[3]
	alarmName := strings.Join(parts[6:], ":")
	return region, alarmName
}

func stringFromConfig(cfg map[string]interface{}, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}

func floatFromConfig(cfg map[string]interface{}, key string) (float64, bool) {
	if cfg == nil {
		return 0, false
	}
	switch v := cfg[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func intFromConfig(cfg map[string]interface{}, key string) int {
	if cfg == nil {
		return 0
	}
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return 0
}
