package playbooks

import (
	"fmt"
	"log/slog"
	"nudgebee/services/internal/database"
	"nudgebee/services/relay"
	"strings"

	"github.com/google/uuid"
)

// ProxySSHCommand is an ssh_command request for a forager ssh-proxy datasource
// (vm_agent integration), shared by the proxy_ssh_command action and the ssh
// integration's vm_agent branch.
type ProxySSHCommand struct {
	AccountID     string
	DatasourceKey string
	AgentType     string // relay agent type; empty means "proxy"
	Command       string
	Host          string // target host; required by dynamic-mode proxies
	Port          int    // 0 = proxy default
	TimeoutMs     int    // 0 = proxy default
}

// executeProxyFn is an indirection so tests can assert the relay payload.
var executeProxyFn = relay.ExecuteProxyWithAgentType

// SSHProxyModeStatic / SSHProxyModeDynamic are the values forager's ssh-proxy
// reports as metadata "mode", which relay stores in integrations.labels.
const (
	SSHProxyModeStatic  = "static"
	SSHProxyModeDynamic = "dynamic"
)

// ResolveProxySSHTarget applies the target precedence for a vm_agent
// ssh_command: per-call host > integration `host` config value.
//
// A dynamic-mode proxy (no configured host) rejects requests without a host,
// so fail before the relay call. A static-mode proxy ignores params.host and
// dials its configured host; agent-registered static integrations carry no
// `host` config value, so an empty host is only an error in dynamic mode.
func ResolveProxySSHTarget(paramHost, integrationHost string, port int, integrationName, mode string) (string, error) {
	host := strings.TrimSpace(paramHost)
	if host == "" {
		host = strings.TrimSpace(integrationHost)
	}
	if host != "" && (len(host) > 253 || strings.ContainsAny(host, " \t\r\n/@")) {
		return "", fmt.Errorf("invalid ssh host %q", host)
	}
	if port < 0 || port > 65535 {
		return "", fmt.Errorf("invalid ssh port %d: must be 1-65535", port)
	}
	if host == "" && mode == SSHProxyModeDynamic {
		return "", fmt.Errorf("no target host for dynamic ssh integration %s: pass a host", integrationName)
	}
	return host, nil
}

// BuildProxySSHActionParams builds the ssh_command action params (datasource_id
// is added by relay.ExecuteProxy) and the relay timeout in seconds.
func BuildProxySSHActionParams(req ProxySSHCommand) (map[string]any, int) {
	params := map[string]any{"command": req.Command}
	if req.Host != "" {
		params["host"] = req.Host
	}
	if req.Port > 0 {
		params["port"] = req.Port
	}
	timeoutSec := 60
	if req.TimeoutMs > 0 {
		params["timeout_ms"] = req.TimeoutMs
		timeoutSec = (req.TimeoutMs / 1000) + 5
	}
	return params, timeoutSec
}

// ExecuteProxySSHCommand sends ssh_command to the agent that owns the datasource.
func ExecuteProxySSHCommand(req ProxySSHCommand) (map[string]any, error) {
	params, timeoutSec := BuildProxySSHActionParams(req)
	result, err := executeProxyFn(req.AccountID, "ssh_command", req.DatasourceKey, params, timeoutSec, req.AgentType)
	if err != nil {
		return nil, fmt.Errorf("proxy ssh_command failed: %w", err)
	}
	return result, nil
}

// LookupSSHProxyMode returns labels.mode ("static" / "dynamic") for an
// agent-registered ssh integration, or "" when unknown.
func LookupSSHProxyMode(integrationID string) string {
	info := lookupProxySSHIntegrationByID(integrationID)
	return info.Mode
}

// proxySSHIntegration is the subset of an ssh integration needed to resolve the
// target host for proxy_ssh_command.
type proxySSHIntegration struct {
	Name string `db:"name"`
	Host string `db:"host"`
	Mode string `db:"mode"`
}

// lookupProxySSHIntegration finds the ssh integration behind a proxy_ssh_command
// datasource_id (integration UUID, or a datasource_key scoped to the account).
// Best effort: returns a zero value when nothing matches, so callers fall back
// to the pre-existing behaviour (let the agent decide).
var lookupProxySSHIntegration = func(datasourceID, accountID string) proxySSHIntegration {
	if _, err := uuid.Parse(datasourceID); err == nil {
		return lookupProxySSHIntegrationByID(datasourceID)
	}
	if accountID == "" {
		return proxySSHIntegration{}
	}
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		slog.Warn("proxy ssh: database unavailable for integration lookup", "error", err)
		return proxySSHIntegration{}
	}
	var info proxySSHIntegration
	err = dbms.Db.Get(&info, `
		SELECT i.name,
		       COALESCE((SELECT v.value FROM integration_config_values v WHERE v.integration_id = i.id AND v.name = 'host'), '') AS host,
		       COALESCE(i.labels->>'mode', '') AS mode
		FROM integrations i
		JOIN integration_config_values dk ON dk.integration_id = i.id AND dk.name = 'datasource_key' AND dk.value = $1
		JOIN integrations_cloud_accounts ica ON ica.integration_id = i.id AND ica.cloud_account_id = $2
		WHERE i.type = 'ssh'
		LIMIT 1`, datasourceID, accountID)
	if err != nil {
		return proxySSHIntegration{}
	}
	return info
}

func lookupProxySSHIntegrationByID(integrationID string) proxySSHIntegration {
	if _, err := uuid.Parse(integrationID); err != nil {
		return proxySSHIntegration{}
	}
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		slog.Warn("proxy ssh: database unavailable for integration lookup", "error", err)
		return proxySSHIntegration{}
	}
	var info proxySSHIntegration
	err = dbms.Db.Get(&info, `
		SELECT i.name,
		       COALESCE((SELECT v.value FROM integration_config_values v WHERE v.integration_id = i.id AND v.name = 'host'), '') AS host,
		       COALESCE(i.labels->>'mode', '') AS mode
		FROM integrations i
		WHERE i.id = $1`, integrationID)
	if err != nil {
		return proxySSHIntegration{}
	}
	return info
}
