package alertrule

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"nudgebee/services/common"
	"nudgebee/services/config"
	"nudgebee/services/security"
)

// CloudAlertRuleSource delegates alert rule creation to cloud-collector for AWS/Azure/GCP.
type CloudAlertRuleSource struct{}

func (s *CloudAlertRuleSource) CreateAlertRule(ctx *security.RequestContext, alertConfig AlertRuleConfig) (*AlertRuleResult, error) {
	payload := map[string]interface{}{
		"account_id":      alertConfig.AccountId,
		"name":            alertConfig.Name,
		"alert_type":      alertConfig.AlertType,
		"query":           alertConfig.Query,
		"severity":        alertConfig.Severity,
		"duration":        alertConfig.Duration,
		"enabled":         alertConfig.Enabled,
		"provider_config": alertConfig.ProviderConfig,
	}

	resp, err := cloudCollectorPost("/v1/cloud/create_alert_rule", ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud alert rule: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read cloud-collector response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("cloud-collector API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ExternalRuleId string `json:"external_rule_id"`
		Status         string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse cloud-collector response: %w", err)
	}

	return &AlertRuleResult{
		ExternalRuleId: result.ExternalRuleId,
		Name:           alertConfig.Name,
		Status:         result.Status,
	}, nil
}

func (s *CloudAlertRuleSource) UpdateAlertRule(ctx *security.RequestContext, externalRuleId string, alertConfig AlertRuleConfig) (*AlertRuleResult, error) {
	payload := map[string]interface{}{
		"account_id":       alertConfig.AccountId,
		"external_rule_id": externalRuleId,
		"name":             alertConfig.Name,
		"alert_type":       alertConfig.AlertType,
		"query":            alertConfig.Query,
		"severity":         alertConfig.Severity,
		"duration":         alertConfig.Duration,
		"enabled":          alertConfig.Enabled,
		"provider_config":  alertConfig.ProviderConfig,
	}
	if alertConfig.SetEnabled != nil {
		payload["set_enabled"] = *alertConfig.SetEnabled
	}
	if len(alertConfig.Restore) > 0 {
		payload["restore"] = alertConfig.Restore
	}

	resp, err := cloudCollectorPost("/v1/cloud/update_alert_rule", ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to update cloud alert rule: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read cloud-collector response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud-collector API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return &AlertRuleResult{
		ExternalRuleId: externalRuleId,
		Name:           alertConfig.Name,
		Status:         "updated",
		Previous:       parseCloudUpdatePrevious(respBody),
	}, nil
}

// parseCloudUpdatePrevious reads the replaced values from a cloud-collector update
// response ({"data": {"previous": {...}}}). A collector that predates them
// returns none, and the caller then has nothing exact to revert to.
func parseCloudUpdatePrevious(body []byte) map[string]any {
	var envelope struct {
		Data struct {
			Previous map[string]any `json:"previous"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	return envelope.Data.Previous
}

func (s *CloudAlertRuleSource) DeleteAlertRule(ctx *security.RequestContext, accountId string, externalRuleId string) error {
	payload := map[string]interface{}{
		"account_id":       accountId,
		"external_rule_id": externalRuleId,
	}

	resp, err := cloudCollectorPost("/v1/cloud/delete_alert_rule", ctx, payload)
	if err != nil {
		return fmt.Errorf("failed to delete cloud alert rule: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloud-collector API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func cloudCollectorPost(path string, ctx *security.RequestContext, payload interface{}) (*http.Response, error) {
	url := config.Config.CloudCollectorServerUrl + path

	headers := map[string]string{
		"Content-Type": "application/json",
		config.Config.CloudCollectorServerTokenHeader: config.Config.CloudCollectorServerToken,
		"x-tenant-id": ctx.GetSecurityContext().GetTenantId(),
	}

	return common.HttpPost(url, common.HttpWithHeaders(headers), common.HttpWithJsonBody(payload))
}
