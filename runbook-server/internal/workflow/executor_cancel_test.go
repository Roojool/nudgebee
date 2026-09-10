package workflow

import (
	"context"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"nudgebee/runbook/internal/model"
)

// TestCanceledRunRecordsCanceledStatus locks in the fix for #37544: a canceled
// run must still write a terminal status to its workflow row. Every activity
// scheduled on a canceled workflow.Context fails on the spot, so before the fix
// the final status update never landed and the Automations list kept the run at
// "Running" no matter how often the user clicked Cancel.
func (s *ExecutorTestSuite) TestCanceledRunRecordsCanceledStatus() {
	var recorded []model.WorkflowExecutionStatus
	s.env.RegisterActivityWithOptions(func(ctx context.Context, wf *model.Workflow, status model.WorkflowExecutionStatus, message string) error {
		recorded = append(recorded, status)
		return nil
	}, activity.RegisterOptions{Name: "Internal_UpdateLastExecutionStatusActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, workflowID string) (map[string]any, error) {
		return map[string]any{}, nil
	}, activity.RegisterOptions{Name: "FetchWorkflowStateActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, tenantID, accountID string, restrictToAccount bool) (FetchConfigsResponse, error) {
		return FetchConfigsResponse{}, nil
	}, activity.RegisterOptions{Name: "FetchWorkflowConfigsActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, typeRefID string, failed bool) error {
		return nil
	}, activity.RegisterOptions{Name: "Internal_UpdateEventResolutionStatusActivity"})

	// The task the run is parked on when the cancel arrives.
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]any) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return map[string]string{"data": "finished"}, nil
		}
	}, activity.RegisterOptions{Name: "core.print"})

	mockStore := new(MockWorkflowStore)
	mockStore.On("GetState", mock.Anything, mock.Anything).Return([]model.WorkflowStateItem{}, nil).Maybe()
	mockStore.On("SetLastExecutionStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	executor := &WorkflowExecutor{workflowStore: mockStore}

	wf := &model.Workflow{
		ID:        "cancel-wf",
		TenantID:  "test-tenant",
		AccountID: "test-account",
		Definition: model.WorkflowDefinition{
			Tasks: []model.Task{{ID: "long", Type: "core.print", Params: map[string]any{"message": "waiting"}}},
		},
	}

	s.env.RegisterDelayedCallback(func() { s.env.CancelWorkflow() }, time.Second)
	s.env.RegisterWorkflow(executor.ExecuteWorkflowInternal)
	s.env.ExecuteWorkflow(executor.ExecuteWorkflowInternal, wf, nil)

	s.True(s.env.IsWorkflowCompleted(), "canceled run should finish rather than hang")
	s.Contains(recorded, model.WorkflowExecutionStatusCanceled, "a canceled run must record CANCELED on its workflow row, not stay RUNNING")
	s.NotContains(recorded, model.WorkflowExecutionStatusFailed, "a user cancel is not a failure")
}
