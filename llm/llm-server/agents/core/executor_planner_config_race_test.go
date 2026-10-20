package core

import (
	"fmt"
	"sync"
	"testing"

	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
)

// A parallel batch resolves each action's tool config against one shared executor.
// Before configMu, two actions for the same tool wrote QueryConfig.ToolConfigs at the
// same moment and the Go runtime aborted the process with "concurrent map writes",
// taking every other conversation on the pod with it. Run under -race.
func TestPlannerExecutor_ConcurrentToolConfigResolutionIsRaceFree(t *testing.T) {
	e := &plannerExecutor{
		agentRequest: NBAgentRequest{
			QueryConfig: toolcore.NBQueryConfig{ToolConfigs: map[string]string{}},
		},
	}

	const goroutines = 16
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		// Writer: the single-config branch of followupForMultipleToolConfigs.
		go func(i int) {
			defer wg.Done()
			e.configMu.Lock()
			defer e.configMu.Unlock()
			e.agentRequest.QueryConfig.ToolConfigs = ensureToolConfigs(e.agentRequest.QueryConfig.ToolConfigs)
			e.agentRequest.QueryConfig.ToolConfigs["gcloud_execute"] = fmt.Sprintf("project-%d", i)
			recordConfigSelectionStrategy(&e.agentRequest.QueryConfig, "gcloud_execute", "sole_config")
		}(i)
		// Reader: shellWriteTarget / perCallWriteTarget on the same parallel path.
		// A plain map read racing a write is the same fatal error as two writes.
		go func() {
			defer wg.Done()
			_ = e.resolvedToolConfig("gcloud_execute")
		}()
	}
	wg.Wait()

	assert.NotEmpty(t, e.resolvedToolConfig("gcloud_execute"), "a config must survive the batch")
	assert.Contains(t, e.agentRequest.QueryConfig.ToolConfigMetadata, "gcloud_execute",
		"the strategy metadata writer shares the map and must be covered by the same lock")
}

// resolvedToolConfig takes configMu, so calling it while holding the lock would
// self-deadlock — sync.Mutex is not reentrant. This pins the sequential contract.
func TestPlannerExecutor_ResolvedToolConfigReleasesTheLock(t *testing.T) {
	e := &plannerExecutor{
		agentRequest: NBAgentRequest{
			QueryConfig: toolcore.NBQueryConfig{ToolConfigs: map[string]string{"kubectl_execute": "prod"}},
		},
	}
	assert.Equal(t, "prod", e.resolvedToolConfig("kubectl_execute"))
	assert.Equal(t, "prod", e.resolvedToolConfig("kubectl_execute"))
	assert.Equal(t, "", e.resolvedToolConfig("absent"))
}
