package integrations

import (
	"nudgebee/services/eventrule/playbooks"
	"nudgebee/services/integrations/core"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stubSSHVMAgent(t *testing.T, mode string) *[]playbooks.ProxySSHCommand {
	t.Helper()
	origMode, origExec := sshProxyModeLookup, executeProxySSH
	t.Cleanup(func() { sshProxyModeLookup, executeProxySSH = origMode, origExec })

	calls := &[]playbooks.ProxySSHCommand{}
	sshProxyModeLookup = func(string) string { return mode }
	executeProxySSH = func(req playbooks.ProxySSHCommand) (map[string]any, error) {
		*calls = append(*calls, req)
		return map[string]any{"stdout": "Linux vm", "exit_code": float64(0)}, nil
	}
	return calls
}

func vmAgentIntegration(extra ...core.IntegrationConfigValue) core.IntegrationDto {
	return core.IntegrationDto{
		Id:   "3f2d6c1e-8a7b-4c1d-9e0f-1a2b3c4d5e6f",
		Name: "fleet-ssh",
		Configs: append([]core.IntegrationConfigValue{
			{Name: "connection_mode", Value: "vm_agent"},
			{Name: "agent_type", Value: "proxy"},
			{Name: "datasource_key", Value: "local:fleet:ssh"},
		}, extra...),
	}
}

func TestSSH_ExecuteViaProxyAgent_ForwardsHost(t *testing.T) {
	calls := stubSSHVMAgent(t, playbooks.SSHProxyModeDynamic)

	resp, err := SSH{}.executeViaProxyAgent("acct-1", vmAgentIntegration(), sshParams{Command: "uname -a", Host: "192.168.0.10", Port: 2222})
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.Equal(t, playbooks.ProxySSHCommand{
		AccountID:     "acct-1",
		DatasourceKey: "local:fleet:ssh",
		AgentType:     "proxy",
		Command:       "uname -a",
		Host:          "192.168.0.10",
		Port:          2222,
	}, (*calls)[0])
	assert.Equal(t, "uname -a", resp["command"])
	assert.Equal(t, "Linux vm", resp["stdout"])
}

func TestSSH_ExecuteViaProxyAgent_HostPrecedence(t *testing.T) {
	intg := vmAgentIntegration(core.IntegrationConfigValue{Name: "host", Value: "10.0.0.5"})
	cases := []struct {
		name   string
		params sshParams
		want   string
	}{
		{"host param wins", sshParams{Command: "x", Host: "10.0.0.9", HostName: "10.0.0.8"}, "10.0.0.9"},
		{"host_name param", sshParams{Command: "x", HostName: "10.0.0.8"}, "10.0.0.8"},
		{"integration host fallback", sshParams{Command: "x"}, "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubSSHVMAgent(t, playbooks.SSHProxyModeDynamic)
			_, err := SSH{}.executeViaProxyAgent("acct-1", intg, tc.params)
			require.NoError(t, err)
			require.Len(t, *calls, 1)
			assert.Equal(t, tc.want, (*calls)[0].Host)
		})
	}
}

func TestSSH_ExecuteViaProxyAgent_DynamicWithoutHostFailsBeforeRelay(t *testing.T) {
	calls := stubSSHVMAgent(t, playbooks.SSHProxyModeDynamic)

	_, err := SSH{}.executeViaProxyAgent("acct-1", vmAgentIntegration(), sshParams{Command: "uptime"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no target host for dynamic ssh integration fleet-ssh")
	assert.Empty(t, *calls)
}

// Static-host agent integrations (e.g. static-vm) have no host config
// value; the agent dials its configured host, so no host is sent.
func TestSSH_ExecuteViaProxyAgent_StaticWithoutHost(t *testing.T) {
	calls := stubSSHVMAgent(t, playbooks.SSHProxyModeStatic)

	_, err := SSH{}.executeViaProxyAgent("acct-1", vmAgentIntegration(), sshParams{Command: "uptime"})
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.Equal(t, "", (*calls)[0].Host)
}

// User-created vm_agent integrations may lack datasource_key/agent_type; the
// integration id is the datasource key and the relay defaults to "proxy".
func TestSSH_ExecuteViaProxyAgent_DatasourceKeyFallback(t *testing.T) {
	calls := stubSSHVMAgent(t, "")
	intg := core.IntegrationDto{
		Id:      "3f2d6c1e-8a7b-4c1d-9e0f-1a2b3c4d5e6f",
		Name:    "static-vm",
		Configs: []core.IntegrationConfigValue{{Name: "connection_mode", Value: "vm_agent"}, {Name: "host", Value: "198.51.100.42"}},
	}
	_, err := SSH{}.executeViaProxyAgent("acct-1", intg, sshParams{Command: "uptime"})
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	assert.Equal(t, intg.Id, (*calls)[0].DatasourceKey)
	assert.Equal(t, "", (*calls)[0].AgentType)
	assert.Equal(t, "198.51.100.42", (*calls)[0].Host)
}

// k8s-mode integrations must keep going through the workspace-pod template:
// the vm_agent branch keys only on connection_mode=vm_agent.
func TestSSH_ConnectionModeRouting(t *testing.T) {
	assert.True(t, sshUsesProxyAgent(vmAgentIntegration().Configs))
	for _, cfgs := range [][]core.IntegrationConfigValue{
		{{Name: "connection_mode", Value: "k8s"}, {Name: "k8s_secret", Value: "s"}},
		{{Name: "host", Value: "nb-dev-db"}, {Name: "k8s_secret", Value: "s"}},
	} {
		assert.False(t, sshUsesProxyAgent(cfgs))
	}
}

func TestSSH_K8sHostAlias(t *testing.T) {
	configs := []core.IntegrationConfigValue{{Name: "host", Value: "10.0.0.5"}, {Name: "k8s_secret", Value: "s"}}

	_, host, err := resolveSSHTarget(configs, withK8sHostAlias(sshParams{Command: "x", Host: "10.0.0.9"}))
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9", host, "host param overrides the saved host on the k8s path")

	_, host, err = resolveSSHTarget(configs, withK8sHostAlias(sshParams{Command: "x", Host: "10.0.0.9", HostName: "10.0.0.8"}))
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.8", host, "host_name wins over the host alias")

	_, host, err = resolveSSHTarget(configs, withK8sHostAlias(sshParams{Command: "x"}))
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5", host)

	for _, bad := range []string{"10.0.0.1;id", "$(id)", "root@10.0.0.1", "-oProxyCommand=x"} {
		_, _, err := resolveSSHTarget(configs, withK8sHostAlias(sshParams{Command: "x", Host: bad}))
		assert.Error(t, err, bad)
	}
}

func TestSSH_BuildK8sSSHCommand(t *testing.T) {
	const prefix = `mkdir -p ~/.ssh && echo "$SSH_KEY" > ~/.ssh/id_rsa && chmod 600 ~/.ssh/id_rsa && ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10`
	assert.Equal(t, prefix+` $SSH_USER@$SSH_HOST 'uptime'`, buildK8sSSHCommand("$SSH_USER@$SSH_HOST", 0, "uptime"),
		"port 0 keeps the previous template byte-for-byte")
	assert.Equal(t, prefix+` -p 2222 ubuntu@10.0.0.9 'uptime'`, buildK8sSSHCommand("ubuntu@10.0.0.9", 2222, "uptime"))
}
