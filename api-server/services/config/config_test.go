package config

import (
	"os"
	"testing"
)

// The API/worker split hangs entirely off this predicate: it decides whether a
// pod starts the RabbitMQ consumers (common.MqConsume) and the EE background
// goroutines (api.RunEEStartupHooks). Getting it wrong in the "false" direction
// on every pod means queues fill with nobody draining them, and — because the
// queues are non-durable — a broker restart then discards the backlog silently.
// So it fails open: only the one explicit opt-out value disables background
// jobs, and anything unrecognised keeps them running.
func TestRunsBackgroundJobs(t *testing.T) {
	original := Config.ServerRole
	t.Cleanup(func() { Config.ServerRole = original })

	tests := []struct {
		name string
		role string
		want bool
	}{
		{"all runs background jobs", ServerRoleAll, true},
		{"worker runs background jobs", ServerRoleWorker, true},
		{"api does not", ServerRoleAPI, false},
		{"empty fails open", "", true},
		{"typo fails open", "apu", true},
		{"case mismatch fails open", "API", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Config.ServerRole = tt.role
			if got := RunsBackgroundJobs(); got != tt.want {
				t.Errorf("RunsBackgroundJobs() with SERVER_ROLE=%q = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

// The default must keep every single-replica, OSS and on-prem install on the
// historical single-deployment behaviour: one process doing both halves.
//
// Config is populated by this package's init(), which has already run by the
// time any test body executes — so t.Setenv cannot make this hermetic, and an
// ambient SERVER_ROLE would make it assert against the environment rather than
// against the default. Skip in that case instead of failing on someone's shell.
func TestServerRoleDefaultsToAll(t *testing.T) {
	if v, ok := os.LookupEnv("SERVER_ROLE"); ok {
		t.Skipf("SERVER_ROLE=%q is set in the environment; the compiled-in default is not observable here", v)
	}
	if Config.ServerRole != ServerRoleAll {
		t.Errorf("default SERVER_ROLE = %q, want %q", Config.ServerRole, ServerRoleAll)
	}
}

// Per-resource knowledge-graph updates are on unless an environment explicitly
// turns them off. They shipped off, and two days on dev showed the detector
// publishing refs that never reached the patch path — so the comparison the
// switch was waiting for could not happen. This pins the default; flipping it
// back to false silently would stop per-resource updates everywhere again.
func TestResourcePatchEnabledByDefault(t *testing.T) {
	if v, ok := os.LookupEnv("KG_RESOURCE_PATCH_ENABLED"); ok {
		t.Skipf("KG_RESOURCE_PATCH_ENABLED=%q is set in the environment; the compiled-in default is not observable here", v)
	}
	if !Config.KGResourcePatchEnabled {
		t.Error("default kg_resource_patch_enabled = false, want true")
	}
}
