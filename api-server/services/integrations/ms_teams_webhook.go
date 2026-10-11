package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nudgebee/services/config"
	"nudgebee/services/integrations/core"
	"nudgebee/services/security"
)

func init() {
	core.RegisterIntegration(MsTeamsWebhook{})
}

const (
	IntegrationMsTeamsWebhook = "ms_teams_webhook"

	MsTeamsWebhookConfigURL         = "webhook_url"
	MsTeamsWebhookConfigInstalledBy = "installed_by"
)

// Hosts Microsoft's Power Automate webhook trigger issues URLs on, plus the
// retired classic O365 connector (existing URLs still deliver). A pasted URL
// elsewhere is rejected at save rather than failing on first send. Widen as
// Microsoft changes hosting.
var msTeamsWebhookAllowedHostSuffixes = []string{
	".logic.azure.com",
	".azure-automation.net",
	".powerplatform.com",
	".powerautomate.com",
	".webhook.office.com",
}

// MsTeamsWebhook is a one-way delivery target: an Adaptive Card POSTed to a Teams
// channel via a Power Automate URL. Many per tenant (no unique index on
// integrations.name); the URL is the encrypted webhook_url config value.
type MsTeamsWebhook struct{}

func (MsTeamsWebhook) Name() string {
	return IntegrationMsTeamsWebhook
}

func (MsTeamsWebhook) Category() core.IntegrationCategory {
	return core.IntegrationCategoryMessaging
}

func (MsTeamsWebhook) ConfigSchema() core.IntegrationSchema {
	return core.IntegrationSchema{
		Type:     core.ToolSchemaTypeObject,
		Required: []string{MsTeamsWebhookConfigURL},
		Testable: true,
		Properties: map[string]core.IntegrationSchemaProperty{
			MsTeamsWebhookConfigURL: {
				Type: core.ToolSchemaTypeString,
				Description: "The webhook URL from the Teams channel's \"Post to a channel when a webhook " +
					"request is received\" workflow (Workflows → + New flow). Not the retired " +
					"Office 365 connector URL.",
				IsEncrypted: true,
				IsTestable:  true,
				Priority:    90,
			},
			MsTeamsWebhookConfigInstalledBy: {
				Type:   core.ToolSchemaTypeString,
				Hidden: true,
			},
		},
	}
}

// ValidateConfig checks URL shape only; the live probe is TestConnection.
func (MsTeamsWebhook) ValidateConfig(_ *security.SecurityContext, values []core.IntegrationConfigValue, _ string) []error {
	raw := strings.TrimSpace(core.ConfigValue(values, MsTeamsWebhookConfigURL))
	if raw == "" {
		return []error{fmt.Errorf("webhook_url is required")}
	}
	if err := validateMsTeamsWebhookURL(raw); err != nil {
		return []error{err}
	}
	return nil
}

// TestConnection posts an Adaptive Card to the webhook (Power Automate answers
// 202, the classic connector 200); a 4xx/5xx body is surfaced to the operator.
func (MsTeamsWebhook) TestConnection(sc *security.RequestContext, values []core.IntegrationConfigValue, _ string) error {
	raw := strings.TrimSpace(core.ConfigValue(values, MsTeamsWebhookConfigURL))
	if err := validateMsTeamsWebhookURL(raw); err != nil {
		return err
	}

	// Threaded in by the connection-test handlers so the card can name the target.
	name := strings.TrimSpace(core.ConfigValue(values, core.IntegrationConfigName))
	text := msTeamsWebhookTestCardText(config.Config.BrandingName, name)

	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body": []any{
			map[string]any{
				"type": "TextBlock",
				"text": text,
				"wrap": true,
			},
		},
	}
	body, _ := json.Marshal(map[string]any{
		"type": "message",
		"attachments": []any{
			map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content":     card,
			},
		},
	})

	ctx := context.Background()
	if sc != nil {
		ctx = sc.GetContext()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the Teams webhook — check the URL and that the flow is turned on: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("the Teams webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// Worded as a test message, not a status claim (a passing test only proves this
// one POST was accepted). brand is empty on white-label installs without it set.
func msTeamsWebhookTestCardText(brand, name string) string {
	if brand == "" {
		brand = "Nudgebee"
	}
	if name != "" {
		return fmt.Sprintf("%s test message for webhook target %q. Notification rules routed here will post to this channel.", brand, name)
	}
	return fmt.Sprintf("%s test message. Notification rules routed to this webhook target will post here.", brand)
}

func validateMsTeamsWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("webhook_url must be a valid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhook_url must be https")
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range msTeamsWebhookAllowedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf(
		"webhook_url host %q is not a Microsoft Teams / Power Automate webhook host — "+
			"paste the URL from the channel's \"Post to a channel when a webhook request is received\" workflow",
		host)
}
