package integrations

import (
	"strings"
	"testing"

	"nudgebee/services/integrations/core"
)

func webhookValues(url string) []core.IntegrationConfigValue {
	return []core.IntegrationConfigValue{{Name: MsTeamsWebhookConfigURL, Value: url}}
}

func TestMsTeamsWebhook_ValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"power automate logic apps", "https://prod-1.westus.logic.azure.com/workflows/abc/triggers/manual/paths/invoke", false},
		{"power automate host", "https://x.powerautomate.com/hook/abc", false},
		{"classic connector still allowed", "https://tenant.webhook.office.com/webhookb2/guid@guid/IncomingWebhook/x/y", false},
		{"empty", "", true},
		{"http not https", "http://x.logic.azure.com/hook", true},
		{"wrong host", "https://evil.example.com/hook", true},
		{"garbage", "://nope", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := MsTeamsWebhook{}.ValidateConfig(nil, webhookValues(tc.url), "")
			if tc.wantErr && len(errs) == 0 {
				t.Fatalf("expected a validation error for %q, got none", tc.url)
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Fatalf("expected %q to validate, got %v", tc.url, errs)
			}
		})
	}
}

func TestMsTeamsWebhook_TestConnectionRejectsDisallowedHostBeforeDialing(t *testing.T) {
	// A host that fails the allowlist must be rejected without any network call.
	if err := (MsTeamsWebhook{}).TestConnection(nil, webhookValues("https://evil.example.com/x"), ""); err == nil {
		t.Fatal("expected disallowed host to fail TestConnection")
	}
}

func TestMsTeamsWebhookTestCardText(t *testing.T) {
	withName := msTeamsWebhookTestCardText("Acme", "Prod alerts — #ops")
	if !strings.Contains(withName, "Acme") || !strings.Contains(withName, "Prod alerts — #ops") {
		t.Fatalf("named card should carry brand and target name, got %q", withName)
	}
	if strings.Contains(strings.ToLower(withName), "connected") {
		t.Fatalf("card must not claim a connection state, got %q", withName)
	}
	noName := msTeamsWebhookTestCardText("", "")
	if !strings.Contains(noName, "Nudgebee") {
		t.Fatalf("empty brand should fall back to Nudgebee, got %q", noName)
	}
}

func TestMsTeamsWebhook_SchemaShape(t *testing.T) {
	s := MsTeamsWebhook{}.ConfigSchema()
	if !s.Testable {
		t.Fatal("schema should be Testable")
	}
	prop, ok := s.Properties[MsTeamsWebhookConfigURL]
	if !ok || !prop.IsEncrypted {
		t.Fatalf("webhook_url must be an encrypted property, got %+v", prop)
	}
	if (MsTeamsWebhook{}).Category() != core.IntegrationCategoryMessaging {
		t.Fatalf("category should be messaging, got %s", (MsTeamsWebhook{}).Category())
	}
}
