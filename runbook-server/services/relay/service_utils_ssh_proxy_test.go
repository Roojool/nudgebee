package relay

import (
	"errors"
	"nudgebee/runbook/services/integrations"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func vmAgentSSHIntegration(values ...integrations.IntegrationConfigValue) integrations.IntegrationConfig {
	base := []integrations.IntegrationConfigValue{
		{Name: "connection_mode", Value: "vm_agent"},
		{Name: "agent_type", Value: "proxy"},
		{Name: "datasource_key", Value: "local:fleet:ssh"},
	}
	return integrations.IntegrationConfig{
		Id:     "3f2d6c1e-8a7b-4c1d-9e0f-1a2b3c4d5e6f",
		Name:   "fleet-ssh",
		Values: append(base, values...),
	}
}

// stubSSHProxy replaces the relay call and the labels.mode lookup for one test
// and returns a pointer to the captured relay body.
func stubSSHProxy(t *testing.T, mode string) *ActionExecuteBody {
	t.Helper()
	origRelay, origMode := executeRelayFn, sshProxyModeLookup
	t.Cleanup(func() { executeRelayFn, sshProxyModeLookup = origRelay, origMode })

	captured := &ActionExecuteBody{}
	executeRelayFn = func(body ActionExecuteBody) (map[string]any, error) {
		*captured = body
		return map[string]any{"data": `{"stdout":"ok","stderr":"","exit_code":0}`}, nil
	}
	sshProxyModeLookup = func(string) string { return mode }
	return captured
}

func TestExecuteSSHViaProxyAgentRunbook_ForwardsTaskHost(t *testing.T) {
	captured := stubSSHProxy(t, "dynamic")

	_, err := executeSSHViaProxyAgentRunbook(vmAgentSSHIntegration(), "uname -a", "acct-1",
		map[string]any{"host": "192.168.0.10", "port": float64(2222)})
	require.NoError(t, err)

	assert.Equal(t, "ssh_command", captured.ActionName)
	assert.Equal(t, "proxy", captured.AgentType)
	assert.Equal(t, "acct-1", captured.AccountID)
	assert.Equal(t, "local:fleet:ssh", captured.ActionParams["datasource_id"])
	assert.Equal(t, "uname -a", captured.ActionParams["command"])
	assert.Equal(t, "192.168.0.10", captured.ActionParams["host"])
	assert.Equal(t, float64(2222), captured.ActionParams["port"])
}

func TestExecuteSSHViaProxyAgentRunbook_TaskHostOverridesIntegrationHost(t *testing.T) {
	captured := stubSSHProxy(t, "dynamic")

	intg := vmAgentSSHIntegration(integrations.IntegrationConfigValue{Name: "host", Value: "10.0.0.5"})
	_, err := executeSSHViaProxyAgentRunbook(intg, "uptime", "acct-1", map[string]any{"host": "10.0.0.9"})
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9", captured.ActionParams["host"])
}

func TestExecuteSSHViaProxyAgentRunbook_FallsBackToIntegrationHost(t *testing.T) {
	captured := stubSSHProxy(t, "dynamic")

	intg := vmAgentSSHIntegration(integrations.IntegrationConfigValue{Name: "host", Value: "10.0.0.5"})
	_, err := executeSSHViaProxyAgentRunbook(intg, "uptime", "acct-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5", captured.ActionParams["host"])
	_, hasPort := captured.ActionParams["port"]
	assert.False(t, hasPort)
}

func TestExecuteSSHViaProxyAgentRunbook_DynamicWithoutHostFailsBeforeRelay(t *testing.T) {
	stubSSHProxy(t, "dynamic")
	called := false
	executeRelayFn = func(ActionExecuteBody) (map[string]any, error) {
		called = true
		return nil, errors.New("should not be called")
	}

	_, err := executeSSHViaProxyAgentRunbook(vmAgentSSHIntegration(), "uptime", "acct-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no target host for dynamic ssh integration fleet-ssh")
	assert.False(t, called, "relay must not be called without a target host")
}

// Agent-registered static-host proxies carry no `host` config value; the agent
// dials its own configured host, so the request must go out unchanged.
func TestExecuteSSHViaProxyAgentRunbook_StaticWithoutHostUnchanged(t *testing.T) {
	for _, mode := range []string{"static", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			captured := stubSSHProxy(t, mode)
			_, err := executeSSHViaProxyAgentRunbook(vmAgentSSHIntegration(), "uptime", "acct-1", nil)
			require.NoError(t, err)
			_, hasHost := captured.ActionParams["host"]
			assert.False(t, hasHost)
			assert.Equal(t, "local:fleet:ssh", captured.ActionParams["datasource_id"])
		})
	}
}

func TestExecuteSSHViaProxyAgentRunbook_RejectsInvalidTarget(t *testing.T) {
	stubSSHProxy(t, "dynamic")
	cases := []map[string]any{
		{"host": "root@10.0.0.1"},
		{"host": "10.0.0.1 -oProxyCommand=x"},
		{"host": "10.0.0.1", "port": float64(70000)},
		{"host": "10.0.0.1", "port": "ssh"},
	}
	for _, c := range cases {
		_, err := executeSSHViaProxyAgentRunbook(vmAgentSSHIntegration(), "uptime", "acct-1", c)
		assert.Error(t, err, "%v", c)
	}
}

func TestParseSSHPort(t *testing.T) {
	tests := []struct {
		in      any
		want    int
		wantErr bool
	}{
		{nil, 0, false},
		{"", 0, false},
		{float64(22), 22, false},
		{22, 22, false},
		{"2222", 2222, false},
		{float64(22.5), 0, true},
		{-1, 0, true},
		{65536, 0, true},
		{"abc", 0, true},
		{true, 0, true},
	}
	for _, tt := range tests {
		got, err := ParseSSHPort(tt.in)
		if tt.wantErr {
			assert.Error(t, err, "%v", tt.in)
			continue
		}
		require.NoError(t, err, "%v", tt.in)
		assert.Equal(t, tt.want, got, "%v", tt.in)
	}
}

func TestBuildK8sSSHCommand(t *testing.T) {
	const prefix = `mkdir -p ~/.ssh && echo "$SSH_KEY" > ~/.ssh/id_rsa && chmod 600 ~/.ssh/id_rsa && ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR`

	// No override: byte-identical to the previous template.
	got, err := buildK8sSSHCommand("uptime", nil)
	require.NoError(t, err)
	assert.Equal(t, prefix+` $SSH_USER@$SSH_HOST 'uptime'`, got)

	got, err = buildK8sSSHCommand("uptime", map[string]any{"host": "10.0.0.9", "port": float64(2222)})
	require.NoError(t, err)
	assert.Equal(t, prefix+` -p 2222 $SSH_USER@10.0.0.9 'uptime'`, got)

	got, err = buildK8sSSHCommand("echo 'x'", map[string]any{"host": "vm-1.internal"})
	require.NoError(t, err)
	assert.Equal(t, prefix+` $SSH_USER@vm-1.internal 'echo '\''x'\'''`, got)

	for _, bad := range []string{"10.0.0.1;rm -rf /", "$(id)", "root@10.0.0.1", "-oProxyCommand=x", "a b"} {
		_, err := buildK8sSSHCommand("uptime", map[string]any{"host": bad})
		assert.Error(t, err, bad)
	}
	_, err = buildK8sSSHCommand("uptime", map[string]any{"host": "10.0.0.1", "port": "22;id"})
	assert.Error(t, err)
}
