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
	core.RegisterIntegration(SlackWebhook{})
}

const (
	IntegrationSlackWebhook = "slack_webhook"

	SlackWebhookConfigURL         = "webhook_url"
	SlackWebhookConfigInstalledBy = "installed_by"
)

// Slack issues incoming-webhook URLs only on this host. A pasted URL elsewhere
// is rejected at save rather than failing on first send.
const slackWebhookAllowedHost = "hooks.slack.com"

// SlackWebhook is a one-way delivery target: a Block Kit message POSTed to a
// Slack channel via an Incoming Webhook URL. Many per tenant (no unique index
// on integrations.name); the URL is the encrypted webhook_url config value.
// Distinct from the "slack" type, which is the OAuth bot install.
type SlackWebhook struct{}

func (SlackWebhook) Name() string {
	return IntegrationSlackWebhook
}

func (SlackWebhook) Category() core.IntegrationCategory {
	return core.IntegrationCategoryMessaging
}

func (SlackWebhook) ConfigSchema() core.IntegrationSchema {
	return core.IntegrationSchema{
		Type:     core.ToolSchemaTypeObject,
		Required: []string{SlackWebhookConfigURL},
		Testable: true,
		Properties: map[string]core.IntegrationSchemaProperty{
			SlackWebhookConfigURL: {
				Type: core.ToolSchemaTypeString,
				Description: "The Incoming Webhook URL for the Slack channel (Slack app config → " +
					"Incoming Webhooks → Add New Webhook to Workspace). Bound to a single channel.",
				IsEncrypted: true,
				IsTestable:  true,
				Priority:    90,
			},
			SlackWebhookConfigInstalledBy: {
				Type:   core.ToolSchemaTypeString,
				Hidden: true,
			},
		},
	}
}

// ValidateConfig checks URL shape only; the live probe is TestConnection.
func (SlackWebhook) ValidateConfig(_ *security.SecurityContext, values []core.IntegrationConfigValue, _ string) []error {
	raw := strings.TrimSpace(core.ConfigValue(values, SlackWebhookConfigURL))
	if raw == "" {
		return []error{fmt.Errorf("webhook_url is required")}
	}
	if err := validateSlackWebhookURL(raw); err != nil {
		return []error{err}
	}
	return nil
}

// TestConnection posts a text message to the webhook; Slack answers 200 with a
// plain "ok" body on success, and a plain-text error string otherwise.
func (SlackWebhook) TestConnection(sc *security.RequestContext, values []core.IntegrationConfigValue, _ string) error {
	raw := strings.TrimSpace(core.ConfigValue(values, SlackWebhookConfigURL))
	if err := validateSlackWebhookURL(raw); err != nil {
		return err
	}

	// Threaded in by the connection-test handlers so the message can name the target.
	name := strings.TrimSpace(core.ConfigValue(values, core.IntegrationConfigName))
	text := slackWebhookTestMessageText(config.Config.BrandingName, name)

	body, _ := json.Marshal(map[string]any{"text": text})

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
		return fmt.Errorf("could not reach the Slack webhook — check the URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("the Slack webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// Worded as a test message, not a status claim (a passing test only proves this
// one POST was accepted). brand is empty on white-label installs without it set.
func slackWebhookTestMessageText(brand, name string) string {
	if brand == "" {
		brand = "Nudgebee"
	}
	if name != "" {
		return fmt.Sprintf("%s test message for webhook target %q. Notification rules routed here will post to this channel.", brand, name)
	}
	return fmt.Sprintf("%s test message. Notification rules routed to this webhook target will post here.", brand)
}

func validateSlackWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("webhook_url must be a valid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhook_url must be https")
	}
	if strings.ToLower(u.Hostname()) != slackWebhookAllowedHost {
		return fmt.Errorf(
			"webhook_url host %q is not a Slack incoming webhook host — "+
				"paste the URL from Slack's Incoming Webhooks app configuration (hooks.slack.com)",
			u.Hostname())
	}
	return nil
}
