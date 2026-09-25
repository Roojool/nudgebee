package integrations

import (
	"nudgebee/services/integrations/core"
	"nudgebee/services/relay"
	"nudgebee/services/security"
)

func init() {
	core.RegisterIntegration(LokiAgent{})
}

type LokiAgent struct{}

func (m LokiAgent) Name() string {
	return "loki"
}

func (m LokiAgent) Category() core.IntegrationCategory {
	return core.IntegrationCategoryLog
}

func (m LokiAgent) ConfigSchema() core.IntegrationSchema {
	return core.IntegrationSchema{
		Type:     core.ToolSchemaTypeObject,
		Required: []string{},
		Properties: map[string]core.IntegrationSchemaProperty{
			"default_log_provider": {
				Type:        core.ToolSchemaTypeBoolean,
				Description: "Set as default log provider for this account",
				Default:     false,
				Priority:    18,
			},
		},
	}
}

func (m LokiAgent) ValidateConfig(sc *security.SecurityContext, config []core.IntegrationConfigValue, accountId string) []error {
	return []error{}
}

// TestConnection implements core.TestableIntegration. The agent's Loki is
// configured in the agent's Helm values, not in this form, so the probe asks the
// agent itself to query it, and reports why it could not.
func (m LokiAgent) TestConnection(sc *security.RequestContext, config []core.IntegrationConfigValue, accountId string) error {
	return relay.TestAgentLoki(accountId)
}
