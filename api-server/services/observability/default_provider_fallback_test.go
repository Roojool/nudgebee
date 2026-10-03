package observability

import (
	"errors"
	"testing"

	"nudgebee/services/account"
	"nudgebee/services/integrations/core"
	"nudgebee/services/security"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetLogsMetricsTracesProvider_SingleActiveProviderDefault(t *testing.T) {
	promURL := "http://prometheus:9090"
	connectedAgent := account.AgentDetails{
		Status: "CONNECTED",
		Features: account.AgentDetailsFeatures{
			PrometheusUrl: &promURL,
		},
	}

	connected, disconnected := true, false
	healthyAgent := connectedAgent
	healthyAgent.Features.PrometheusConnection = &connected
	downAgent := connectedAgent
	downAgent.Features.PrometheusConnection = &disconnected

	tests := []struct {
		name               string
		activeIntegrations []core.IntegrationDto
		agentDetails       *account.AgentDetails
		agentErr           error
		hasDisabledRow     bool
		expectErr          bool
		expectedErrSubstr  string
		expectedProvider   string
		expectedSource     string
	}{
		{
			name: "single enabled agent row plus connected agent -> provider is prometheus",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-1", Type: "prometheus", Source: "agent"},
			},
			agentDetails:     &connectedAgent,
			hasDisabledRow:   false,
			expectedProvider: "prometheus",
			expectedSource:   "agent",
		},
		{
			name: "connected agent with prometheus AND secondary datadog integration -> agent prometheus takes priority",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-1", Type: "prometheus", Source: "agent"},
				{Id: "int-2", Type: "datadog", Source: "user"},
			},
			agentDetails:     &connectedAgent,
			hasDisabledRow:   false,
			expectedProvider: "prometheus",
			expectedSource:   "agent",
		},
		{
			name: "single enabled user datadog row without agent -> provider is datadog",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-2", Type: "datadog", Source: "user"},
			},
			agentDetails:     nil,
			agentErr:         errors.New("no agent connected"),
			hasDisabledRow:   false,
			expectedProvider: "datadog",
			expectedSource:   "user",
		},
		{
			name: "two user integrations of same type without agent -> choice error naming both",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-1", Name: "prometheus-prod", Type: "prometheus", Source: "user"},
				{Id: "int-2", Name: "prometheus-staging", Type: "prometheus", Source: "user"},
			},
			agentDetails:      nil,
			agentErr:          errors.New("no agent connected"),
			hasDisabledRow:    false,
			expectErr:         true,
			expectedErrSubstr: "multiple metrics providers are configured (prometheus-prod, prometheus-staging)",
		},
		{
			name: "two different telemetry integrations without agent -> choice error naming both",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-1", Name: "datadog-user", Type: "datadog", Source: "user"},
				{Id: "int-2", Name: "prometheus-user", Type: "prometheus", Source: "user"},
			},
			agentDetails:      nil,
			agentErr:          errors.New("no agent connected"),
			hasDisabledRow:    false,
			expectErr:         true,
			expectedErrSubstr: "multiple metrics providers are configured (datadog-user, prometheus-user)",
		},
		{
			name:               "a disabled agent row without other providers -> no provider",
			activeIntegrations: []core.IntegrationDto{},
			agentDetails:       &connectedAgent,
			hasDisabledRow:     true,
			expectedProvider:   "",
			expectedSource:     "",
		},
		{
			// The shared agent row was last written by another cluster whose Prometheus is
			// off; this account's own agent reports Prometheus healthy.
			name: "disabled shared agent row but this agent reports prometheus connected -> provider is prometheus",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-2", Name: "Datadog-hive", Type: "datadog", Source: "user"},
				{Id: "int-3", Name: "new relic integration", Type: "newrelic", Source: "user"},
			},
			agentDetails:     &healthyAgent,
			hasDisabledRow:   true,
			expectedProvider: "prometheus",
			expectedSource:   "agent",
		},
		{
			name: "disabled agent row and this agent reports prometheus down -> agent skipped, choice error",
			activeIntegrations: []core.IntegrationDto{
				{Id: "int-2", Name: "Datadog-hive", Type: "datadog", Source: "user"},
				{Id: "int-3", Name: "new relic integration", Type: "newrelic", Source: "user"},
			},
			agentDetails:      &downAgent,
			hasDisabledRow:    true,
			expectErr:         true,
			expectedErrSubstr: "multiple metrics providers are configured (Datadog-hive, new relic integration)",
		},
		{
			name:               "no integrations and connected agent with prometheus -> provider is prometheus",
			activeIntegrations: []core.IntegrationDto{},
			agentDetails:       &connectedAgent,
			hasDisabledRow:     false,
			expectedProvider:   "prometheus",
			expectedSource:     "agent",
		},
		{
			name:               "no integrations and no agent -> returns lookup error",
			activeIntegrations: []core.IntegrationDto{},
			agentDetails:       nil,
			agentErr:           errors.New("no agent connected"),
			hasDisabledRow:     false,
			expectErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patches := gomonkey.NewPatches()
			defer patches.Reset()

			// 1. No integration flagged as default_metrics_provider = true
			patches.ApplyFunc(core.GetIntegrationByConfigNameValues,
				func(_ *security.RequestContext, _, _, _ string) (*core.IntegrationDto, error) {
					return nil, nil
				})

			// 2. Active integrations linked to account
			patches.ApplyFunc(core.ListActiveIntegrationsForAccount,
				func(_ *security.RequestContext, _ string) ([]core.IntegrationDto, error) {
					return tt.activeIntegrations, nil
				})

			// 3. Agent connection details
			patches.ApplyFunc(account.GetAgentConnectionDetails,
				func(_ string) (account.AgentDetails, error) {
					if tt.agentErr != nil {
						return account.AgentDetails{}, tt.agentErr
					}
					if tt.agentDetails != nil {
						return *tt.agentDetails, nil
					}
					return account.AgentDetails{}, nil
				})

			// 4. HasOnlyDisabledAccountSourceIntegration checks if a disabled record exists
			patches.ApplyFunc(core.HasOnlyDisabledAccountSourceIntegration,
				func(_ *security.RequestContext, _, _, _ string) (bool, error) {
					return tt.hasDisabledRow, nil
				})

			// 5. Fallback source lookup
			patches.ApplyFunc(core.GetIntegrationByTypePreferringDefault,
				func(_ *security.RequestContext, _, _, _ string) (*core.IntegrationDto, error) {
					return nil, nil
				})

			ctx := tenantCarryingContext("test-tenant")
			provider, source, _, err := getLogsMetricsTracesProviderWithIntegration(ctx, "acc-1", "", "metrics", "")
			if tt.expectErr {
				require.Error(t, err)
				if tt.expectedErrSubstr != "" {
					assert.Contains(t, err.Error(), tt.expectedErrSubstr)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedProvider, provider)
			assert.Equal(t, tt.expectedSource, source)
		})
	}
}

func TestGetTelemetrySourceForAccount_AmbiguousProviderError(t *testing.T) {
	patches := gomonkey.NewPatches()
	defer patches.Reset()

	patches.ApplyFunc(core.GetIntegrationByConfigNameValues,
		func(_ *security.RequestContext, _, _, _ string) (*core.IntegrationDto, error) {
			return nil, nil
		})

	patches.ApplyFunc(account.GetAgentConnectionDetails,
		func(_ string) (account.AgentDetails, error) {
			return account.AgentDetails{}, errors.New("no agent connected")
		})

	patches.ApplyFunc(core.HasOnlyDisabledAccountSourceIntegration,
		func(_ *security.RequestContext, _, _, _ string) (bool, error) {
			return false, nil
		})

	patches.ApplyFunc(core.GetIntegrationByTypePreferringDefault,
		func(_ *security.RequestContext, _, _, _ string) (*core.IntegrationDto, error) {
			return nil, nil
		})

	ctx := tenantCarryingContext("test-tenant")

	// 1. Metrics query with ambiguous providers (two prometheus integrations)
	patches.ApplyFunc(core.ListActiveIntegrationsForAccount,
		func(_ *security.RequestContext, _ string) ([]core.IntegrationDto, error) {
			return []core.IntegrationDto{
				{Id: "int-1", Name: "prometheus-prod", Type: "prometheus", Source: "user"},
				{Id: "int-2", Name: "prometheus-staging", Type: "prometheus", Source: "user"},
			}, nil
		})
	_, err := getMetricsSourceForAccount(ctx, "acc-1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observability: multiple metrics providers are configured (prometheus-prod, prometheus-staging), but none is marked as default")
	assert.Contains(t, err.Error(), "please specify metrics_provider or choose a default in Settings")

	// 2. Logs query when only datadog is configured without agent -> auto-defaults to datadog (no error)
	patches.ApplyFunc(core.ListActiveIntegrationsForAccount,
		func(_ *security.RequestContext, _ string) ([]core.IntegrationDto, error) {
			return []core.IntegrationDto{
				{Id: "int-1", Name: "datadog-logs", Type: "datadog", Source: "user"},
			}, nil
		})
	logSource, err := getLogSourceForAccount(ctx, "acc-1", "", "")
	require.NoError(t, err)
	assert.NotNil(t, logSource)

	// 3. Traces query when agent is connected without trace provider and only prometheus integration exists -> returns standard required error
	patches.ApplyFunc(account.GetAgentConnectionDetails,
		func(_ string) (account.AgentDetails, error) {
			return account.AgentDetails{Status: "CONNECTED"}, nil
		})
	patches.ApplyFunc(core.ListActiveIntegrationsForAccount,
		func(_ *security.RequestContext, _ string) ([]core.IntegrationDto, error) {
			return []core.IntegrationDto{
				{Id: "int-1", Name: "prometheus-prod", Type: "prometheus", Source: "user"},
			}, nil
		})
	_, err = getTraceSourceForAccount(ctx, "acc-1", "", "")
	require.Error(t, err)
	assert.Equal(t, "observability: trace provider (trace_provider) is required", err.Error())

	// 4. Multiple logs providers (datadog + loki) -> returns ambiguous error naming both
	patches.ApplyFunc(core.ListActiveIntegrationsForAccount,
		func(_ *security.RequestContext, _ string) ([]core.IntegrationDto, error) {
			return []core.IntegrationDto{
				{Id: "int-1", Name: "datadog-prod", Type: "datadog", Source: "user"},
				{Id: "int-2", Name: "loki-prod", Type: "loki", Source: "user"},
			}, nil
		})
	_, err = getLogSourceForAccount(ctx, "acc-1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observability: multiple logs providers are configured (datadog-prod, loki-prod), but none is marked as default")
	assert.Contains(t, err.Error(), "please specify log_provider or choose a default in Settings")

	// 5. Multiple traces providers (datadog + jaeger) -> returns ambiguous error naming both
	patches.ApplyFunc(core.ListActiveIntegrationsForAccount,
		func(_ *security.RequestContext, _ string) ([]core.IntegrationDto, error) {
			return []core.IntegrationDto{
				{Id: "int-1", Name: "datadog-prod", Type: "datadog", Source: "user"},
				{Id: "int-2", Name: "jaeger-prod", Type: "jaeger", Source: "user"},
			}, nil
		})
	_, err = getTraceSourceForAccount(ctx, "acc-1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "observability: multiple traces providers are configured (datadog-prod, jaeger-prod), but none is marked as default")
	assert.Contains(t, err.Error(), "please specify trace_provider or choose a default in Settings")
}
