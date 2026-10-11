package workflow

import (
	"context"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"nudgebee/runbook/internal/model"
)

// TestMatrixDependencyKeepsSiblingDependencies covers a task that depends on a
// matrix task AND on another task (here a slow "gate", standing in for a
// core.approval). Rewriting the matrix dependency into its expanded IDs used to
// append into the DependsOn slice it was still reading, which overwrote the
// gate entry: ["scan", "gate"] became ["scan-0", "scan-1", "scan-1"]. The tail
// task then ran as soon as the matrix finished, without waiting for the gate.
func (s *ExecutorTestSuite) TestMatrixDependencyKeepsSiblingDependencies() {
	s.registerFanInActivities()
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]any) (any, error) {
		return map[string]string{"data": "gate-done"}, nil
	}, activity.RegisterOptions{Name: "test.gate"})
	s.env.OnActivity("test.gate", mock.Anything, mock.Anything).
		After(30*time.Second).
		Return(map[string]string{"data": "gate-done"}, nil)
	executor := s.newFanInExecutor()

	wf := &model.Workflow{
		ID:        "test-matrix-sibling-deps-wf",
		TenantID:  "test-tenant",
		AccountID: "test-account",
		Definition: model.WorkflowDefinition{
			Tasks: []model.Task{
				{
					ID:     "scan",
					Type:   "core.print",
					Matrix: map[string]any{"target": []any{"a", "b"}},
					Params: map[string]any{"message": "scanned"},
				},
				{ID: "gate", Type: "test.gate", Params: map[string]any{}},
				{
					ID:        "tail",
					Type:      "data.transform",
					DependsOn: []string{"scan", "gate"},
					Params: map[string]any{
						"expression": "{{ Tasks['gate'].status | default('-') }}/{{ Tasks['gate'].output.data | default('-') }}",
					},
				},
			},
			Output: map[string]any{
				"tail": "{{ Tasks['tail'].output.data }}",
			},
		},
	}

	s.env.RegisterWorkflow(executor.ExecuteWorkflowInternal)
	s.env.ExecuteWorkflow(executor.ExecuteWorkflowInternal, wf, nil)

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())

	var resultStr string
	s.NoError(s.env.GetWorkflowResult(&resultStr))
	// tail must render after gate completed, so it sees gate's status and output.
	s.JSONEq(`{"tail":"COMPLETED/gate-done"}`, resultStr)
}
