package playbooks

import (
	"fmt"
	"nudgebee/services/common"
)

type proxySSHCommandAction struct{}

type proxySSHCommandParams struct {
	DatasourceID string `json:"datasource_id"`
	Command      string `json:"command"`
	TimeoutMs    int    `json:"timeout_ms,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	// Host/Port target a VM through a dynamic-mode ssh-proxy. Host overrides
	// the integration's saved host; the agent enforces its allowed_hosts.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

func (a *proxySSHCommandAction) Execute(ctx PlaybookActionContext, rawParams map[string]any) (PlaybookActionResponse, error) {
	var params proxySSHCommandParams
	if err := common.UnmarshalMapToStruct(rawParams, &params); err != nil {
		return nil, fmt.Errorf("failed to unmarshal params: %w", err)
	}

	if params.DatasourceID == "" {
		return nil, fmt.Errorf("datasource_id is required")
	}
	if params.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	accountID := params.AccountID
	if accountID == "" {
		accountID = resolveAccountIDForIntegration(params.DatasourceID)
	}
	if accountID == "" {
		accountID = ctx.GetAccountId()
	}
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}

	datasourceKey, err := resolveDatasourceKey(params.DatasourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve datasource: %w", err)
	}

	integration := lookupProxySSHIntegration(params.DatasourceID, accountID)
	integrationName := integration.Name
	if integrationName == "" {
		integrationName = params.DatasourceID
	}
	host, err := ResolveProxySSHTarget(params.Host, integration.Host, params.Port, integrationName, integration.Mode)
	if err != nil {
		return nil, err
	}

	result, err := ExecuteProxySSHCommand(ProxySSHCommand{
		AccountID:     accountID,
		DatasourceKey: datasourceKey,
		Command:       params.Command,
		Host:          host,
		Port:          params.Port,
		TimeoutMs:     params.TimeoutMs,
	})
	if err != nil {
		return nil, err
	}

	title, _ := rawParams["title"].(string)
	if title == "" {
		title = "SSH Command"
	}

	return NewPlaybookActionResponseJson(
		result,
		map[string]any{"title": title},
		nil,
		map[string]any{"query-result-version": "1.0", "command": params.Command},
	), nil
}
