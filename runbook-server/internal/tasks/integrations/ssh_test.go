package integrations

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSHTargetConfigs(t *testing.T) {
	assert.Equal(t, map[string]any{}, sshTargetConfigs(map[string]any{"command": "uptime"}))
	assert.Equal(t, map[string]any{}, sshTargetConfigs(map[string]any{"host": "", "port": ""}))
	assert.Equal(t,
		map[string]any{"host": "192.168.0.10", "port": float64(2222)},
		sshTargetConfigs(map[string]any{"host": "192.168.0.10", "port": float64(2222)}))
}

func TestSSHTaskInputSchemaHasHost(t *testing.T) {
	props := (&SSHTask{}).InputSchema().Properties
	assert.Contains(t, props, "host")
	assert.False(t, props["host"].Required)
	assert.Contains(t, props, "port")
}
