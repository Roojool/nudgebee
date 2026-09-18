package integrations

// webhookMessagingTypes are the one-way channel-webhook integration types (as
// opposed to OAuth bot installs). Every "does this change need a messaging
// cache invalidate" check in actions_integration.go tests membership here, so
// a new webhook messaging type joins that behavior just by being added.
var webhookMessagingTypes = map[string]bool{
	IntegrationMsTeamsWebhook:    true,
	IntegrationSlackWebhook:      true,
	IntegrationGoogleChatWebhook: true,
}

// IsWebhookMessagingIntegration reports whether name is a one-way channel
// webhook integration type.
func IsWebhookMessagingIntegration(name string) bool {
	return webhookMessagingTypes[name]
}
