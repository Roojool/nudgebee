package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/llm/config"
	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
)

// dynamicSSHConfig is a forager ssh_access integration as ListToolConfigs
// loads it: vm_agent values from relay, proxy metadata in the labels.
func dynamicSSHConfig(name, allowedHosts string) core.ToolConfig {
	return core.ToolConfig{
		Id:   name + "-id",
		Name: name,
		Values: []core.ToolConfigValue{
			{Name: "connection_mode", Value: "vm_agent"},
			{Name: "agent_type", Value: "proxy"},
			{Name: "datasource_key", Value: "local:" + name + ":ssh"},
		},
		Tags: map[string]string{"mode": "dynamic", "port": "22", "allowed_hosts": allowedHosts},
	}
}

// stubVMs replaces the cloud_resourses lookup with fixed VMs per integration.
func stubVMs(t *testing.T, byConfig map[string][]discoveredVM) *int {
	t.Helper()
	calls := 0
	previous := sshVMLister
	sshVMLister = func(_ *security.RequestContext, cfg core.ToolConfig) ([]discoveredVM, error) {
		calls++
		return byConfig[cfg.Name], nil
	}
	t.Cleanup(func() { sshVMLister = previous })
	return &calls
}

func testRequestContext() *security.RequestContext {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return security.NewRequestContext(context.Background(), security.NewSecurityContextForSuperAdmin(), logger, nil, nil)
}

func instanceRequest(instance string) core.NBToolCallRequest {
	return core.NBToolCallRequest{Arguments: map[string]any{"instance": instance, "args": "df -h"}}
}

var fleetVMs = []discoveredVM{
	{ResourceID: "i-0a1b2c3d4e5f60718", Name: "web-1", IP: "192.168.0.10", DNSName: "ip-192-168-0-10.ec2.internal", AccountID: "acct-fleet"},
	{ResourceID: "vm-192.168.0.11", Name: "192.168.0.11", IP: "192.168.0.11", AccountID: "acct-fleet"},
	{ResourceID: "i-0outside", Name: "db-1", IP: "10.9.0.5", AccountID: "acct-fleet"},
}

func TestServerIdentifyConfig_ResolvesDiscoveredVM(t *testing.T) {
	stubVMs(t, map[string][]discoveredVM{"site-a-ssh": fleetVMs})
	fleet := dynamicSSHConfig("site-a-ssh", "[192.168.0.0/24]")
	configs := []core.ToolConfig{
		{Name: "static-vm", Values: []core.ToolConfigValue{{Name: "host", Value: "billing.example.com"}}},
		fleet,
	}
	ctx := core.NbToolContext{Ctx: testRequestContext(), AccountId: "acct-fleet"}

	for _, instance := range []string{"i-0a1b2c3d4e5f60718", "I-0A1B2C3D4E5F60718", "192.168.0.10", "web-1", "ip-192-168-0-10.ec2.internal."} {
		t.Run(instance, func(t *testing.T) {
			cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest(instance), configs)
			require.NoError(t, err)
			assert.Equal(t, "site-a-ssh", cfg.Name)
			assert.Equal(t, "192.168.0.10", getConfigValue(cfg.Values, "host"))
		})
	}

	t.Run("placeholder row by IP", func(t *testing.T) {
		cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("192.168.0.11"), configs)
		require.NoError(t, err)
		assert.Equal(t, "192.168.0.11", getConfigValue(cfg.Values, "host"))
	})

	t.Run("does not change the loaded config", func(t *testing.T) {
		_, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("web-1"), configs)
		require.NoError(t, err)
		assert.Empty(t, getConfigValue(configs[1].Values, "host"))
	})

	t.Run("unknown VM falls through", func(t *testing.T) {
		cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("i-unknown"), configs)
		require.NoError(t, err)
		assert.Empty(t, cfg.Name)
	})
}

func TestServerIdentifyConfig_NameMatchWinsOverVMLookup(t *testing.T) {
	calls := stubVMs(t, map[string][]discoveredVM{"site-a-ssh": fleetVMs})
	configs := []core.ToolConfig{dynamicSSHConfig("site-a-ssh", "[192.168.0.0/24]")}

	cfg, err := ServerExecuteTool{}.IdentifyConfig(core.NbToolContext{Ctx: testRequestContext()}, instanceRequest("site-a-ssh"), configs)
	require.NoError(t, err)
	assert.Equal(t, "site-a-ssh", cfg.Name)
	assert.Empty(t, getConfigValue(cfg.Values, "host"))
	assert.Zero(t, *calls, "a name match must not query VMs")
}

func TestServerIdentifyConfig_VMOutsideAllowedHosts(t *testing.T) {
	stubVMs(t, map[string][]discoveredVM{"site-a-ssh": fleetVMs, "no-scope-ssh": fleetVMs})
	ctx := core.NbToolContext{Ctx: testRequestContext(), AccountId: "acct-fleet"}

	t.Run("outside the CIDR", func(t *testing.T) {
		configs := []core.ToolConfig{dynamicSSHConfig("site-a-ssh", "[192.168.0.0/24]")}
		cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("db-1"), configs)
		require.ErrorIs(t, err, core.ErrNoMatchingConfig)
		assert.Contains(t, err.Error(), "no ssh access for db-1")
		assert.Empty(t, cfg.Name)
	})

	t.Run("no allowed_hosts label fails closed", func(t *testing.T) {
		configs := []core.ToolConfig{dynamicSSHConfig("no-scope-ssh", "")}
		_, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("192.168.0.10"), configs)
		require.ErrorIs(t, err, core.ErrNoMatchingConfig)
		assert.Contains(t, err.Error(), "no ssh access for 192.168.0.10")
	})

	t.Run("exact host entry", func(t *testing.T) {
		configs := []core.ToolConfig{dynamicSSHConfig("site-a-ssh", `["10.9.0.5"]`)}
		cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("i-0outside"), configs)
		require.NoError(t, err)
		assert.Equal(t, "10.9.0.5", getConfigValue(cfg.Values, "host"))
	})
}

func TestServerIdentifyConfig_IgnoresNonDynamicIntegrations(t *testing.T) {
	calls := stubVMs(t, map[string][]discoveredVM{"static-ssh": fleetVMs})
	static := dynamicSSHConfig("static-ssh", "[192.168.0.0/24]")
	static.Tags["mode"] = "static"

	cfg, err := ServerExecuteTool{}.IdentifyConfig(core.NbToolContext{Ctx: testRequestContext()}, instanceRequest("web-1"), []core.ToolConfig{static})
	require.NoError(t, err)
	assert.Empty(t, cfg.Name)
	assert.Zero(t, *calls)
}

func TestServerIdentifyConfig_Ambiguity(t *testing.T) {
	shared := discoveredVM{ResourceID: "i-0a1b2c3d4e5f60718", IP: "192.168.0.10"}
	onFleet, onVuln := shared, shared
	onFleet.AccountID, onVuln.AccountID = "acct-fleet", "acct-vuln"
	stubVMs(t, map[string][]discoveredVM{
		"site-a-ssh": {onFleet},
		"site-b-ssh": {onVuln},
	})
	configs := []core.ToolConfig{
		dynamicSSHConfig("site-a-ssh", "[192.168.0.0/24]"),
		dynamicSSHConfig("site-b-ssh", "[192.168.0.0/16]"),
	}

	t.Run("prefers the conversation's account", func(t *testing.T) {
		ctx := core.NbToolContext{Ctx: testRequestContext(), AccountId: "acct-vuln"}
		cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("i-0a1b2c3d4e5f60718"), configs)
		require.NoError(t, err)
		assert.Equal(t, "site-b-ssh", cfg.Name)
		assert.Equal(t, "192.168.0.10", getConfigValue(cfg.Values, "host"))
	})

	t.Run("lists candidates otherwise", func(t *testing.T) {
		ctx := core.NbToolContext{Ctx: testRequestContext(), AccountId: "acct-other"}
		for i := 0; i < 5; i++ {
			cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("i-0a1b2c3d4e5f60718"), configs)
			require.ErrorIs(t, err, core.ErrNoMatchingConfig)
			assert.Empty(t, cfg.Name, "never an arbitrary pick")
			assert.Contains(t, err.Error(), "site-a-ssh (host 192.168.0.10")
			assert.Contains(t, err.Error(), "site-b-ssh (host 192.168.0.10")
		}
	})

	t.Run("one name, two VMs in one integration", func(t *testing.T) {
		stubVMs(t, map[string][]discoveredVM{"site-a-ssh": {
			{ResourceID: "i-a", Name: "web", IP: "192.168.0.10", AccountID: "acct-fleet"},
			{ResourceID: "i-b", Name: "web", IP: "192.168.0.12", AccountID: "acct-fleet"},
		}})
		ctx := core.NbToolContext{Ctx: testRequestContext(), AccountId: "acct-fleet"}
		_, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("web"), configs[:1])
		require.ErrorIs(t, err, core.ErrNoMatchingConfig)
		assert.Contains(t, err.Error(), "192.168.0.12")
	})

	t.Run("duplicate rows for one IP are one target", func(t *testing.T) {
		stubVMs(t, map[string][]discoveredVM{"site-a-ssh": {
			{ResourceID: "i-a", Name: "web", IP: "192.168.0.10", AccountID: "acct-fleet"},
			{ResourceID: "vm-192.168.0.10", Name: "web", IP: "192.168.0.10", AccountID: "acct-fleet"},
		}})
		ctx := core.NbToolContext{Ctx: testRequestContext(), AccountId: "acct-fleet"}
		cfg, err := ServerExecuteTool{}.IdentifyConfig(ctx, instanceRequest("web"), configs[:1])
		require.NoError(t, err)
		assert.Equal(t, "192.168.0.10", getConfigValue(cfg.Values, "host"))
	})
}

func TestParseAllowedHosts(t *testing.T) {
	assert.Equal(t, []string{"10.0.0.0/24", "host-a"}, parseAllowedHosts("[10.0.0.0/24 host-a]"))
	assert.Equal(t, []string{"10.0.0.0/24", "host-a"}, parseAllowedHosts(`["10.0.0.0/24","host-a"]`))
	assert.Equal(t, []string{"10.0.0.0/24", "host-a"}, parseAllowedHosts("10.0.0.0/24, host-a"))
	assert.Empty(t, parseAllowedHosts(""))
	assert.Empty(t, parseAllowedHosts("[]"))
}

func TestHostAllowed(t *testing.T) {
	allowed := []string{"192.168.0.0/24", "10.1.1.1", "bastion.internal"}
	assert.True(t, hostAllowed("192.168.0.200", allowed))
	assert.True(t, hostAllowed("10.1.1.1", allowed))
	assert.True(t, hostAllowed("BASTION.internal", allowed))
	assert.False(t, hostAllowed("192.168.1.1", allowed))
	assert.False(t, hostAllowed("10.1.1.2", allowed))
	assert.False(t, hostAllowed("192.168.0.10", nil), "an empty allow list allows nothing")
}

func TestProxySSHTargetHost_Precedence(t *testing.T) {
	withHost := withHostValue(dynamicSSHConfig("fleet-ssh", "[192.168.0.0/24]"), "192.168.0.10")

	host, err := proxySSHTargetHost(withHost, map[string]any{"host_name": "192.168.0.99"})
	require.NoError(t, err)
	assert.Equal(t, "192.168.0.99", host, "per-call host_name wins")

	host, err = proxySSHTargetHost(withHost, nil)
	require.NoError(t, err)
	assert.Equal(t, "192.168.0.10", host, "config host next")

	host, err = proxySSHTargetHost(dynamicSSHConfig("fleet-ssh", ""), map[string]any{})
	require.NoError(t, err)
	assert.Empty(t, host)

	_, err = proxySSHTargetHost(withHost, map[string]any{"host_name": "1.2.3.4; rm -rf /"})
	require.Error(t, err)
}

// relayCapture is a fake relay that records ssh_command params.
type relayCapture struct {
	requests []map[string]any
}

func startRelay(t *testing.T) *relayCapture {
	t.Helper()
	capture := &relayCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Body struct {
				ActionName   string         `json:"action_name"`
				ActionParams map[string]any `json:"action_params"`
			} `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capture.requests = append(capture.requests, body.Body.ActionParams)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": `{"stdout":"ok\n","stderr":"","exit_code":0}`})
	}))
	t.Cleanup(srv.Close)
	orig := config.Config.RelayServerEndpoint
	config.Config.RelayServerEndpoint = srv.URL
	t.Cleanup(func() { config.Config.RelayServerEndpoint = orig })
	return capture
}

func TestExecuteContainerJob_ProxySSHHost(t *testing.T) {
	dynamic := dynamicSSHConfig("site-a-ssh", "[192.168.0.0/24]")
	static := core.ToolConfig{Name: "static-vm", Values: []core.ToolConfigValue{
		{Name: "connection_mode", Value: "vm_agent"}, {Name: "datasource_key", Value: "local:static-vm"},
	}, Tags: map[string]string{"mode": "static", "host": "10.0.0.5"}}

	tests := []struct {
		name     string
		cfg      core.ToolConfig
		configs  map[string]any
		wantHost string // "" = no host param
		wantErr  string
	}{
		{name: "host_name on dynamic", cfg: dynamic, configs: map[string]any{"host_name": "192.168.0.10"}, wantHost: "192.168.0.10"},
		{name: "resolved host on dynamic", cfg: withHostValue(dynamic, "192.168.0.11"), wantHost: "192.168.0.11"},
		{name: "host_name beats resolved host", cfg: withHostValue(dynamic, "192.168.0.11"), configs: map[string]any{"host_name": "192.168.0.12"}, wantHost: "192.168.0.12"},
		{name: "dynamic without host fails before relay", cfg: dynamic, wantErr: "no target host for dynamic ssh integration site-a-ssh"},
		{name: "static stays unchanged", cfg: static},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			relay := startRelay(t)
			toolCtx := core.NbToolContext{AccountId: "acct-fleet", Ctx: testRequestContext(), ToolConfig: tc.cfg}
			out, err := ExecuteContainerJob(toolCtx, RelayJobSSH, "uname -a", "acct-fleet", tc.configs, false)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Empty(t, relay.requests, "must not call relay")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out, "ok")
			require.Len(t, relay.requests, 1)
			params := relay.requests[0]
			assert.Equal(t, "uname -a", params["command"])
			if tc.wantHost == "" {
				assert.NotContains(t, params, "host")
			} else {
				assert.Equal(t, tc.wantHost, params["host"])
			}
		})
	}
}

// server_command_executor on a dynamic integration goes straight to the relay
// (not the workspace shim, which would drop the host), with and without
// host_name.
func TestServerExecuteTool_CallVMAgent(t *testing.T) {
	stubVMs(t, map[string][]discoveredVM{"site-a-ssh": fleetVMs})
	cfg := dynamicSSHConfig("site-a-ssh", "[192.168.0.0/24]")
	call := func(args map[string]any) (core.NBToolResponse, error) {
		ctx := core.NbToolContext{AccountId: "acct-fleet", Ctx: testRequestContext(), ToolConfig: cfg}
		return ServerExecuteTool{}.Call(ctx, core.NBToolCallRequest{Arguments: args})
	}

	t.Run("instance resolves to the VM host", func(t *testing.T) {
		relay := startRelay(t)
		_, err := call(map[string]any{"instance": "i-0a1b2c3d4e5f60718", "args": "df -h"})
		require.NoError(t, err)
		require.Len(t, relay.requests, 1)
		assert.Equal(t, "192.168.0.10", relay.requests[0]["host"])
	})

	t.Run("host_name with the integration name", func(t *testing.T) {
		relay := startRelay(t)
		_, err := call(map[string]any{"instance": "site-a-ssh", "host_name": "192.168.0.11", "args": "uname -a"})
		require.NoError(t, err)
		require.Len(t, relay.requests, 1)
		assert.Equal(t, "192.168.0.11", relay.requests[0]["host"])
	})

	t.Run("integration name without host_name", func(t *testing.T) {
		relay := startRelay(t)
		_, err := call(map[string]any{"instance": "site-a-ssh", "args": "uname -a"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no target host for dynamic ssh integration site-a-ssh")
		assert.Empty(t, relay.requests)
	})

	t.Run("VM outside allowed_hosts", func(t *testing.T) {
		relay := startRelay(t)
		_, err := call(map[string]any{"instance": "db-1", "args": "uname -a"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no ssh access for db-1")
		assert.Empty(t, relay.requests)
	})
}

func TestListSSHIntegrationVMs_Query(t *testing.T) {
	db, mock := mockMetastore(t)
	defer func() { _ = db.Close() }()

	seen := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM cloud_resourses").
		WithArgs("fleet-id", "local:fleet").
		WillReturnRows(sqlmock.NewRows([]string{"resourse_id", "name", "ip", "dns", "account", "last_seen"}).
			AddRow("i-0a1b2c3d4e5f60718", "web-1", "192.168.0.10", "ip-192-168-0-10.ec2.internal", "acct-fleet", seen).
			AddRow("vm-192.168.0.11", "192.168.0.11", "192.168.0.11", "", "acct-fleet", nil))

	cfg := core.ToolConfig{Id: "fleet-id", Name: "fleet-ssh", Values: []core.ToolConfigValue{{Name: "datasource_key", Value: "local:fleet:ssh"}}}
	vms, err := listSSHIntegrationVMs(testRequestContext(), cfg)
	require.NoError(t, err)
	require.Len(t, vms, 2)
	assert.Equal(t, discoveredVM{ResourceID: "i-0a1b2c3d4e5f60718", Name: "web-1", IP: "192.168.0.10", DNSName: "ip-192-168-0-10.ec2.internal", AccountID: "acct-fleet", LastSeen: seen}, vms[0])
	assert.True(t, vms[1].LastSeen.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListSSHIntegrationVMs_QueryError(t *testing.T) {
	db, mock := mockMetastore(t)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("FROM cloud_resourses").WillReturnError(errors.New("boom"))

	_, err := listSSHIntegrationVMs(testRequestContext(), core.ToolConfig{Id: "x", Name: "x-ssh"})
	require.Error(t, err)
}

func TestRenderSSHVMTargets(t *testing.T) {
	cfg := dynamicSSHConfig("site-a-ssh", "[192.168.0.0/16]")
	base := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	var all []vmMatch
	for i := 0; i < 55; i++ {
		all = append(all, vmMatch{Config: cfg, VM: discoveredVM{
			ResourceID: fmt.Sprintf("i-%02d", i), IP: fmt.Sprintf("192.168.1.%d", i), Host: fmt.Sprintf("192.168.1.%d", i), LastSeen: base.Add(time.Duration(i) * time.Minute),
		}})
	}
	// A second row for the newest IP collapses into one entry.
	all = append(all, vmMatch{Config: cfg, VM: discoveredVM{ResourceID: "vm-192.168.1.54", IP: "192.168.1.54", Host: "192.168.1.54", LastSeen: base}})

	ordered := orderVMTargets(all)
	require.Len(t, ordered, 55)
	assert.Equal(t, "i-54", ordered[0].VM.ResourceID, "most recently seen first")

	out := renderSSHVMTargets(ordered)
	assert.True(t, strings.HasPrefix(out, "Discovered VMs reachable over SSH, most recently seen first: i-54 (192.168.1.54, via site-a-ssh), i-53"))
	assert.Contains(t, out, "and 5 more")
	assert.NotContains(t, out, "i-04 (", "capped at 50")
	assert.Contains(t, out, `server_command_executor: pass instance="<instance id, IP or name>"`)
	assert.Empty(t, renderSSHVMTargets(nil))
}

// forager matches a hostname allowed_hosts entry by exact name, so a VM in
// scope only by DNS name must be targeted by that name, not its IP.
func TestReachableVMs_AllowedByDNSNameTargetsName(t *testing.T) {
	stubVMs(t, map[string][]discoveredVM{"fleet-ssh": {
		{ResourceID: "i-a", IP: "192.168.0.10", DNSName: "web-1.corp.internal.", AccountID: "acct-fleet"},
		{ResourceID: "i-b", IP: "192.168.0.11", AccountID: "acct-fleet"},
	}})
	cfg := dynamicSSHConfig("fleet-ssh", "[web-1.corp.internal 10.0.0.0/8]")

	reachable, outOfScope, err := reachableVMs(testRequestContext(), cfg)
	require.NoError(t, err)
	require.Len(t, reachable, 1)
	assert.Equal(t, "web-1.corp.internal", reachable[0].Host)
	require.Len(t, outOfScope, 1)
	assert.Equal(t, "i-b", outOfScope[0].ResourceID)
}

func TestCanReadAccount_FailsClosedWithoutSecurityContext(t *testing.T) {
	assert.False(t, canReadAccount(nil, "acct-fleet"))
}
