package workflow

import (
	"testing"

	"nudgebee/runbook/internal/model"

	"github.com/stretchr/testify/assert"
)

func TestRedactSecretsFromTasks_MasksForeachChildInput(t *testing.T) {
	wfDef := model.WorkflowDefinition{Tasks: []model.Task{
		{ID: "loop", Type: "core.foreach", Params: map[string]any{
			"tasks": []any{
				map[string]any{"id": "printSecret", "params": map[string]any{"message": "{{ Secrets.API_KEY }}"}},
			},
		}},
	}}
	tasks := []model.TaskExecutionDetails{
		{ID: "loop-0-printSecret", Input: map[string]any{"message": "Zx9-canary-7Qw"}, Output: "done"},
		{ID: "loop-1-printSecret", Input: map[string]any{"message": "Zx9-canary-7Qw"}, Output: "done"},
	}

	RedactSecretsFromTasks(tasks, wfDef)

	for _, task := range tasks {
		assert.Equal(t, RedactedValue, task.Input["message"])
		assert.Equal(t, "done", task.Output, "outputs stay readable")
	}
}

// A group keeps its children in params["tasks"]; a child's params that render a
// credential must be masked like any top-level step's.
func TestRedactSecretsFromTasks_MasksGroupChildParamsInParamsTasks(t *testing.T) {
	wfDef := model.WorkflowDefinition{Tasks: []model.Task{
		{ID: "grp", Type: "core.group", Params: map[string]any{"tasks": []any{
			map[string]any{"id": "leak", "type": "core.print", "params": map[string]any{"message": "sees {{ Secrets.k }}"}},
			map[string]any{"id": "sibling", "type": "core.print", "params": map[string]any{"message": "ok"}},
		}}},
	}}
	tasks := []model.TaskExecutionDetails{{ID: "grp", Children: []model.TaskExecutionDetails{
		{ID: "leak", Input: map[string]any{"message": "sees Zx9-canary"}, Output: "sees Zx9-canary"},
		{ID: "sibling", Input: map[string]any{"message": "ok"}, Output: "ok"},
	}}}

	RedactSecretsFromTasks(tasks, wfDef)

	assert.Equal(t, RedactedValue, tasks[0].Children[0].Input["message"])
	assert.Equal(t, "sees Zx9-canary", tasks[0].Children[0].Output, "outputs stay readable")
	assert.Equal(t, "ok", tasks[0].Children[1].Input["message"])
	assert.Equal(t, "ok", tasks[0].Children[1].Output)
}

// A bare-id stub inside a container must not shadow the full definition.
func TestBuildTaskDefinitionMap_StubDoesNotShadowDefinition(t *testing.T) {
	m := buildTaskDefinitionMap([]model.Task{
		{ID: "loop", Type: "core.foreach", Params: map[string]any{"tasks": []any{map[string]any{"id": "inner"}}}},
		{ID: "inner", Type: "core.print", Params: map[string]any{"message": "{{ Secrets.k }}"}},
	})
	assert.Equal(t, "core.print", m["inner"].Type)
}
