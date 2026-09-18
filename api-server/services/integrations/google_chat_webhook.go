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
	core.RegisterIntegration(GoogleChatWebhook{})
}

const (
	IntegrationGoogleChatWebhook = "google_chat_webhook"

	GoogleChatWebhookConfigURL         = "webhook_url"
	GoogleChatWebhookConfigInstalledBy = "installed_by"
)

// Google issues incoming-webhook URLs only on this host (the key/token that
// authorize the post are query params on the URL itself, not this suffix).
const googleChatWebhookAllowedHost = "chat.googleapis.com"

// GoogleChatWebhook is a one-way delivery target: a Chat message POSTed to a
// space via an Incoming Webhook URL. Many per tenant (no unique index on
// integrations.name); the URL is the encrypted webhook_url config value.
// Distinct from the "google_chat_space" type, which is the service-account
// bound-space model.
type GoogleChatWebhook struct{}

func (GoogleChatWebhook) Name() string {
	return IntegrationGoogleChatWebhook
}

func (GoogleChatWebhook) Category() core.IntegrationCategory {
	return core.IntegrationCategoryMessaging
}

func (GoogleChatWebhook) ConfigSchema() core.IntegrationSchema {
	return core.IntegrationSchema{
		Type:     core.ToolSchemaTypeObject,
		Required: []string{GoogleChatWebhookConfigURL},
		Testable: true,
		Properties: map[string]core.IntegrationSchemaProperty{
			GoogleChatWebhookConfigURL: {
				Type: core.ToolSchemaTypeString,
				Description: "The Incoming Webhook URL for the Google Chat space (space name → Apps & " +
					"integrations → Webhooks → Add webhook). Bound to a single space; Workspace accounts only.",
				IsEncrypted: true,
				IsTestable:  true,
				Priority:    90,
			},
			GoogleChatWebhookConfigInstalledBy: {
				Type:   core.ToolSchemaTypeString,
				Hidden: true,
			},
		},
	}
}

// ValidateConfig checks URL shape only; the live probe is TestConnection.
func (GoogleChatWebhook) ValidateConfig(_ *security.SecurityContext, values []core.IntegrationConfigValue, _ string) []error {
	raw := strings.TrimSpace(core.ConfigValue(values, GoogleChatWebhookConfigURL))
	if raw == "" {
		return []error{fmt.Errorf("webhook_url is required")}
	}
	if err := validateGoogleChatWebhookURL(raw); err != nil {
		return []error{err}
	}
	return nil
}

// TestConnection posts a text message to the webhook; Chat answers 200 with a
// JSON message resource on success, and a JSON error body otherwise.
func (GoogleChatWebhook) TestConnection(sc *security.RequestContext, values []core.IntegrationConfigValue, _ string) error {
	raw := strings.TrimSpace(core.ConfigValue(values, GoogleChatWebhookConfigURL))
	if err := validateGoogleChatWebhookURL(raw); err != nil {
		return err
	}

	// Threaded in by the connection-test handlers so the message can name the target.
	name := strings.TrimSpace(core.ConfigValue(values, core.IntegrationConfigName))
	text := googleChatWebhookTestMessageText(config.Config.BrandingName, name)

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

	// Short, explicit timeout: a hung Google Chat call has previously frozen the
	// service's health check by way of an unbounded transport elsewhere.
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the Google Chat webhook — check the URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("the Google Chat webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// Worded as a test message, not a status claim (a passing test only proves this
// one POST was accepted). brand is empty on white-label installs without it set.
func googleChatWebhookTestMessageText(brand, name string) string {
	if brand == "" {
		brand = "Nudgebee"
	}
	if name != "" {
		return fmt.Sprintf("%s test message for webhook target %q. Notification rules routed here will post to this space.", brand, name)
	}
	return fmt.Sprintf("%s test message. Notification rules routed to this webhook target will post here.", brand)
}

func validateGoogleChatWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("webhook_url must be a valid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhook_url must be https")
	}
	if strings.ToLower(u.Hostname()) != googleChatWebhookAllowedHost {
		return fmt.Errorf(
			"webhook_url host %q is not a Google Chat incoming webhook host — "+
				"paste the URL from the space's Apps & integrations → Webhooks (chat.googleapis.com)",
			u.Hostname())
	}
	return nil
}
