package playbooks

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedProxyCall struct {
	called     bool
	accountID  string
	action     string
	datasource string
	params     map[string]any
	timeoutSec int
	agentType  string
}

// stubProxySSH replaces the relay call and integration lookup for one test.
func stubProxySSH(t *testing.T, intg proxySSHIntegration) *capturedProxyCall {
	t.Helper()
	origExec, origLookup := executeProxyFn, lookupProxySSHIntegration
	t.Cleanup(func() { executeProxyFn, lookupProxySSHIntegration = origExec, origLookup })

	c := &capturedProxyCall{}
	executeProxyFn = func(accountID, action, datasourceID string, params map[string]any, timeoutSeconds int, agentType string) (map[string]any, error) {
		*c = capturedProxyCall{true, accountID, action, datasourceID, params, timeoutSeconds, agentType}
		return map[string]any{"stdout": "ok", "stderr": "", "exit_code": float64(0)}, nil
	}
	lookupProxySSHIntegration = func(string, string) proxySSHIntegration { return intg }
	return c
}

func runProxySSH(t *testing.T, params map[string]any) (PlaybookActionResponse, error) {
	t.Helper()
	ctx := NewPlaybookActionContext("tenant-1", "acct-1", nil, PlaybookEvent{})
	return (&proxySSHCommandAction{}).Execute(ctx, params)
}

func TestProxySSHCommand_ForwardsHostAndPort(t *testing.T) {
	c := stubProxySSH(t, proxySSHIntegration{Name: "fleet-ssh", Mode: SSHProxyModeDynamic})

	_, err := runProxySSH(t, map[string]any{
		"datasource_id": "local:fleet:ssh",
		"command":       "uname -a",
		"host":          "192.168.0.10",
		"port":          float64(2222),
		"timeout_ms":    float64(10000),
	})
	require.NoError(t, err)

	require.True(t, c.called)
	assert.Equal(t, "acct-1", c.accountID)
	assert.Equal(t, "ssh_command", c.action)
	assert.Equal(t, "local:fleet:ssh", c.datasource)
	assert.Equal(t, "", c.agentType, "empty agent type routes to proxy")
	assert.Equal(t, 15, c.timeoutSec)
	assert.Equal(t, map[string]any{"command": "uname -a", "host": "192.168.0.10", "port": 2222, "timeout_ms": 10000}, c.params)
}

func TestProxySSHCommand_ParamHostOverridesIntegrationHost(t *testing.T) {
	c := stubProxySSH(t, proxySSHIntegration{Name: "fleet-ssh", Host: "10.0.0.5", Mode: SSHProxyModeDynamic})

	_, err := runProxySSH(t, map[string]any{"datasource_id": "local:fleet:ssh", "command": "uptime", "host": "10.0.0.9"})
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9", c.params["host"])
}

func TestProxySSHCommand_FallsBackToIntegrationHost(t *testing.T) {
	c := stubProxySSH(t, proxySSHIntegration{Name: "fleet-ssh", Host: "10.0.0.5", Mode: SSHProxyModeDynamic})

	_, err := runProxySSH(t, map[string]any{"datasource_id": "local:fleet:ssh", "command": "uptime"})
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5", c.params["host"])
}

func TestProxySSHCommand_DynamicWithoutHostFailsBeforeRelay(t *testing.T) {
	c := stubProxySSH(t, proxySSHIntegration{Name: "fleet-ssh", Mode: SSHProxyModeDynamic})

	_, err := runProxySSH(t, map[string]any{"datasource_id": "local:fleet:ssh", "command": "uptime"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no target host for dynamic ssh integration fleet-ssh")
	assert.False(t, c.called)
}

// Static-host / unknown-mode datasources keep the previous payload: no host.
func TestProxySSHCommand_StaticWithoutHostUnchanged(t *testing.T) {
	for _, mode := range []string{SSHProxyModeStatic, ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			c := stubProxySSH(t, proxySSHIntegration{Name: "static-vm", Mode: mode})
			_, err := runProxySSH(t, map[string]any{"datasource_id": "local:static-vm", "command": "uptime"})
			require.NoError(t, err)
			assert.Equal(t, map[string]any{"command": "uptime"}, c.params)
			assert.Equal(t, 60, c.timeoutSec)
		})
	}
}

func TestProxySSHCommand_RelayErrorWrapped(t *testing.T) {
	stubProxySSH(t, proxySSHIntegration{})
	executeProxyFn = func(string, string, string, map[string]any, int, string) (map[string]any, error) {
		return nil, errors.New("agent not connected")
	}
	_, err := runProxySSH(t, map[string]any{"datasource_id": "local:x", "command": "uptime"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy ssh_command failed: agent not connected")
}

func TestResolveProxySSHTarget(t *testing.T) {
	tests := []struct {
		name, paramHost, intgHost, mode string
		port                            int
		want                            string
		wantErr                         string
	}{
		{name: "param wins", paramHost: "10.0.0.9", intgHost: "10.0.0.5", mode: "dynamic", want: "10.0.0.9"},
		{name: "integration fallback", intgHost: " 10.0.0.5 ", mode: "dynamic", want: "10.0.0.5"},
		{name: "dynamic no host", mode: "dynamic", wantErr: "no target host for dynamic ssh integration x"},
		{name: "static no host", mode: "static", want: ""},
		{name: "unknown mode no host", mode: "", want: ""},
		{name: "reject user@host", paramHost: "root@10.0.0.1", wantErr: "invalid ssh host"},
		{name: "reject whitespace", paramHost: "10.0.0.1 -oProxyCommand=x", wantErr: "invalid ssh host"},
		{name: "reject port", paramHost: "10.0.0.1", port: 70000, wantErr: "invalid ssh port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveProxySSHTarget(tt.paramHost, tt.intgHost, tt.port, "x", tt.mode)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
