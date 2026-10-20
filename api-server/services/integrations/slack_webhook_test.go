package integrations

import (
	"strings"
	"testing"

	"nudgebee/services/integrations/core"
)

func slackWebhookValues(url string) []core.IntegrationConfigValue {
	return []core.IntegrationConfigValue{{Name: SlackWebhookConfigURL, Value: url}}
}

func TestSlackWebhook_ValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"slack incoming webhook", "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXXXXXXXXXX", false},
		{"empty", "", true},
		{"http not https", "http://hooks.slack.com/services/T000/B000/X", true},
		{"wrong host", "https://evil.example.com/services/T000/B000/X", true},
		{"garbage", "://nope", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := SlackWebhook{}.ValidateConfig(nil, slackWebhookValues(tc.url), "")
			if tc.wantErr && len(errs) == 0 {
				t.Fatalf("expected a validation error for %q, got none", tc.url)
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Fatalf("expected %q to validate, got %v", tc.url, errs)
			}
		})
	}
}

func TestSlackWebhook_TestConnectionRejectsDisallowedHostBeforeDialing(t *testing.T) {
	// A host that fails the allowlist must be rejected without any network call.
	if err := (SlackWebhook{}).TestConnection(nil, slackWebhookValues("https://evil.example.com/x"), ""); err == nil {
		t.Fatal("expected disallowed host to fail TestConnection")
	}
}

func TestSlackWebhookTestMessageText(t *testing.T) {
	withName := slackWebhookTestMessageText("Acme", "Prod alerts — #ops")
	if !strings.Contains(withName, "Acme") || !strings.Contains(withName, "Prod alerts — #ops") {
		t.Fatalf("named message should carry brand and target name, got %q", withName)
	}
	if strings.Contains(strings.ToLower(withName), "connected") {
		t.Fatalf("message must not claim a connection state, got %q", withName)
	}
	noName := slackWebhookTestMessageText("", "")
	if !strings.Contains(noName, "Nudgebee") {
		t.Fatalf("empty brand should fall back to Nudgebee, got %q", noName)
	}
}

func TestSlackWebhook_SchemaShape(t *testing.T) {
	s := SlackWebhook{}.ConfigSchema()
	if !s.Testable {
		t.Fatal("schema should be Testable")
	}
	prop, ok := s.Properties[SlackWebhookConfigURL]
	if !ok || !prop.IsEncrypted {
		t.Fatalf("webhook_url must be an encrypted property, got %+v", prop)
	}
	if (SlackWebhook{}).Category() != core.IntegrationCategoryMessaging {
		t.Fatalf("category should be messaging, got %s", (SlackWebhook{}).Category())
	}
}

func TestIsWebhookMessagingIntegration(t *testing.T) {
	if !IsWebhookMessagingIntegration(IntegrationSlackWebhook) {
		t.Fatal("slack_webhook should be a webhook messaging integration")
	}
	if !IsWebhookMessagingIntegration(IntegrationMsTeamsWebhook) {
		t.Fatal("ms_teams_webhook should be a webhook messaging integration")
	}
	if IsWebhookMessagingIntegration("slack") {
		t.Fatal("the OAuth slack bot install must not be treated as a webhook messaging integration")
	}
}
