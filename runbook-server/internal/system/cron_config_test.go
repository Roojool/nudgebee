package system

import (
	"strings"
	"testing"

	"nudgebee/runbook/config"
)

// Scheduled crons are the one kind of services-server traffic that should move
// to the worker pods when the deployment is split: each /rpc-cron handler runs
// its job as a goroutine on whichever pod took the call. The cron endpoint
// override is how that traffic gets there, and falling back to the normal URL
// when it is unset is what keeps every existing install unchanged.
func TestServiceCronEndpoint(t *testing.T) {
	origAPI, origCron := config.Config.ServiceEndpoint, config.Config.ServiceCronEndpoint
	t.Cleanup(func() {
		config.Config.ServiceEndpoint, config.Config.ServiceCronEndpoint = origAPI, origCron
	})

	tests := []struct {
		name string
		cron string
		want string
	}{
		{"unset falls back to the API URL", "", "http://services-server:8000"},
		{"whitespace-only falls back to the API URL", "   ", "http://services-server:8000"},
		{"set routes crons to it", "http://services-server-worker:8000", "http://services-server-worker:8000"},
		{"trailing slash trimmed so /rpc-cron does not double up", "http://services-server-worker:8000/", "http://services-server-worker:8000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.Config.ServiceEndpoint = "http://services-server:8000"
			config.Config.ServiceCronEndpoint = tt.cron
			if got := serviceCronEndpoint(); got != tt.want {
				t.Errorf("serviceCronEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every trigger aimed at services-server must follow the override, and nothing
// aimed at another service may. Asserted against the real embedded YAML so a
// trigger added later — or one written with a hard-coded services-server URL —
// is caught here rather than silently staying on the API pods.
func TestLoadCronTriggersRoutesServicesServerCronsToCronEndpoint(t *testing.T) {
	origAPI, origCron, origCC := config.Config.ServiceEndpoint, config.Config.ServiceCronEndpoint, config.Config.CloudCollectorServerUrl
	t.Cleanup(func() {
		config.Config.ServiceEndpoint, config.Config.ServiceCronEndpoint, config.Config.CloudCollectorServerUrl = origAPI, origCron, origCC
	})
	config.Config.ServiceEndpoint = "http://services-server:8000"
	config.Config.ServiceCronEndpoint = "http://services-server-worker:8000"
	config.Config.CloudCollectorServerUrl = "http://cloud-collector:8000"

	triggers, err := LoadCronTriggers()
	if err != nil {
		t.Fatalf("LoadCronTriggers: %v", err)
	}

	toWorker := 0
	for _, tr := range triggers {
		if strings.Contains(tr.Webhook, "{{") {
			t.Errorf("trigger %q has an unresolved placeholder: %s", tr.Name, tr.Webhook)
		}
		if strings.HasPrefix(tr.Webhook, "http://services-server:") {
			t.Errorf("trigger %q still targets the API Service despite the cron override: %s", tr.Name, tr.Webhook)
		}
		if strings.HasPrefix(tr.Webhook, "http://services-server-worker:8000/") {
			toWorker++
		}
		if strings.Contains(tr.Name, "cloud-account") && !strings.HasPrefix(tr.Webhook, "http://cloud-collector:8000/") {
			t.Errorf("trigger %q should be unaffected by the services-server override, got %s", tr.Name, tr.Webhook)
		}
	}
	if toWorker == 0 {
		t.Fatal("no trigger was routed to the cron endpoint; the override is not being applied")
	}
	t.Logf("%d of %d triggers routed to the cron endpoint", toWorker, len(triggers))
}
