package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveOrchestratorThinkingLevelConfig(t *testing.T) {
	assert.Equal(t, "high", resolveOrchestratorThinkingLevelConfig("high", true), "canonical setting applies")
	assert.Equal(t, "", resolveOrchestratorThinkingLevelConfig("", true), "explicit canonical empty disables")
	assert.Equal(t, "medium", resolveOrchestratorThinkingLevelConfig("", false), "compiled default remains medium")
}
