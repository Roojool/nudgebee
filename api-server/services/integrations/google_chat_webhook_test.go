package integrations

import (
	"strings"
	"testing"

	"nudgebee/services/integrations/core"
)

func googleChatWebhookValues(url string) []core.IntegrationConfigValue {
	return []core.IntegrationConfigValue{{Name: GoogleChatWebhookConfigURL, Value: url}}
}

func TestGoogleChatWebhook_ValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"google chat incoming webhook", "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=k&token=t", false},
		{"empty", "", true},
		{"http not https", "http://chat.googleapis.com/v1/spaces/AAAA/messages?key=k&token=t", true},
		{"wrong host", "https://evil.example.com/v1/spaces/AAAA/messages?key=k&token=t", true},
		{"garbage", "://nope", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := GoogleChatWebhook{}.ValidateConfig(nil, googleChatWebhookValues(tc.url), "")
			if tc.wantErr && len(errs) == 0 {
				t.Fatalf("expected a validation error for %q, got none", tc.url)
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Fatalf("expected %q to validate, got %v", tc.url, errs)
			}
		})
	}
}

func TestGoogleChatWebhook_TestConnectionRejectsDisallowedHostBeforeDialing(t *testing.T) {
	// A host that fails the allowlist must be rejected without any network call.
	if err := (GoogleChatWebhook{}).TestConnection(nil, googleChatWebhookValues("https://evil.example.com/x"), ""); err == nil {
		t.Fatal("expected disallowed host to fail TestConnection")
	}
}

func TestGoogleChatWebhookTestMessageText(t *testing.T) {
	withName := googleChatWebhookTestMessageText("Acme", "Prod alerts — #ops")
	if !strings.Contains(withName, "Acme") || !strings.Contains(withName, "Prod alerts — #ops") {
		t.Fatalf("named message should carry brand and target name, got %q", withName)
	}
	if strings.Contains(strings.ToLower(withName), "connected") {
		t.Fatalf("message must not claim a connection state, got %q", withName)
	}
	noName := googleChatWebhookTestMessageText("", "")
	if !strings.Contains(noName, "Nudgebee") {
		t.Fatalf("empty brand should fall back to Nudgebee, got %q", noName)
	}
}

func TestGoogleChatWebhook_SchemaShape(t *testing.T) {
	s := GoogleChatWebhook{}.ConfigSchema()
	if !s.Testable {
		t.Fatal("schema should be Testable")
	}
	prop, ok := s.Properties[GoogleChatWebhookConfigURL]
	if !ok || !prop.IsEncrypted {
		t.Fatalf("webhook_url must be an encrypted property, got %+v", prop)
	}
	if (GoogleChatWebhook{}).Category() != core.IntegrationCategoryMessaging {
		t.Fatalf("category should be messaging, got %s", (GoogleChatWebhook{}).Category())
	}
}

func TestIsWebhookMessagingIntegration_GoogleChat(t *testing.T) {
	if !IsWebhookMessagingIntegration(IntegrationGoogleChatWebhook) {
		t.Fatal("google_chat_webhook should be a webhook messaging integration")
	}
	if IsWebhookMessagingIntegration("google_chat_space") {
		t.Fatal("the service-account bound-space type must not be treated as a webhook messaging integration")
	}
}
