package executors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSHExecutorTargetConfigs(t *testing.T) {
	assert.Equal(t, map[string]any{}, sshTargetConfigs(ExecutionConfig{}))
	assert.Equal(t,
		map[string]any{"host": "192.168.0.10", "port": 2222},
		sshTargetConfigs(ExecutionConfig{Host: "192.168.0.10", Port: 2222}))
}
