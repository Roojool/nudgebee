package integrations

import (
	"nudgebee/services/integrations/core"
	"nudgebee/services/relay"
	"nudgebee/services/security"
)

func init() {
	core.RegisterIntegration(PrometheusAgent{})
}

type PrometheusAgent struct{}

func (m PrometheusAgent) Name() string {
	return "prometheus"
}

func (m PrometheusAgent) Category() core.IntegrationCategory {
	return core.IntegrationCategoryMetrics
}

func (m PrometheusAgent) ConfigSchema() core.IntegrationSchema {
	return core.IntegrationSchema{
		Type:     core.ToolSchemaTypeObject,
		Required: []string{},
		Properties: map[string]core.IntegrationSchemaProperty{
			"default_metrics_provider": {
				Type:        core.ToolSchemaTypeBoolean,
				Description: "Set as default metrics provider for this account",
				Default:     false,
				Priority:    16,
			},
		},
	}
}

func (m PrometheusAgent) ValidateConfig(sc *security.SecurityContext, config []core.IntegrationConfigValue, accountId string) []error {
	return []error{}
}

// TestConnection implements core.TestableIntegration. The agent's Prometheus has
// no URL in this form — it is configured in the agent's Helm values — so the
// probe asks the agent itself to run a query, and reports why it could not.
func (m PrometheusAgent) TestConnection(sc *security.RequestContext, config []core.IntegrationConfigValue, accountId string) error {
	return relay.TestAgentPrometheus(accountId)
}
