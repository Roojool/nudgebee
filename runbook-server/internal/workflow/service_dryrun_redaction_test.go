package workflow

import (
	"testing"

	"nudgebee/runbook/internal/model"
	"nudgebee/runbook/internal/tasks"
	"nudgebee/runbook/internal/tasks/scripting"
	"nudgebee/runbook/services/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
)

// Dry run usually falls back to querying the trace, because activities are
// skipped and history carries no task rows. That fallback assigned the trace
// straight onto the response without ever redacting it, so the Dry Run button
// showed resolved credentials that the Executions panel hides.
func TestDryRunWorkflow_RedactsTheQueriedTrace(t *testing.T) {
	mockTemporalClient := &MockTemporalClient{}
	dc := converter.GetDefaultDataConverter()
	mockStore := new(MockWorkflowStore)
	registry := tasks.NewInitializedTaskRegistry()
	registry.RegisterTask(&scripting.RunScriptTask{})
	configs := new(MockConfigServiceDryRun)
	configs.On("ListConfigsDecrypted", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]model.Config{
		{Key: "db_password", Value: "Zx9-canary-7Qw", Type: model.ConfigTypeSecret},
	}, nil)
	service := NewService(mockTemporalClient, mockStore, dc, registry, &WorkflowExecutor{
		temporalClient: mockTemporalClient, workflowStore: mockStore, dataConverter: dc,
	}, configs)

	sc := security.NewRequestContextForTenantAccountAdmin("test-tenant", "test-user", []string{"test-account"})

	req := model.DryRunWorkflowRequest{
		Definition: model.WorkflowDefinition{
			Tasks: []model.Task{{
				ID: "task1", Type: "scripting.run_script",
				Params: map[string]any{"script": "echo {{ Secrets['db_password'] }}"},
			}},
		},
	}

	mockRun := &MockWorkflowRun{}
	mockRun.On("Get", mock.Anything, mock.Anything).Return(`{"res":"ok"}`, nil)
	mockTemporalClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(mockRun, nil)

	mockIterator := new(MockHistoryIterator)
	mockIterator.On("HasNext").Return(false)
	mockTemporalClient.On("GetWorkflowHistory", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(mockIterator)

	mockQueryValue := new(MockValue)
	mockQueryValue.On("Get", mock.Anything).Run(func(args mock.Arguments) {
		if ptr, ok := args.Get(0).(*[]model.TaskExecutionDetails); ok {
			*ptr = []model.TaskExecutionDetails{{
				ID:             "task1",
				Type:           "scripting.run_script",
				Status:         model.TaskStatusFailed,
				Input:          map[string]any{"script": "echo Zx9-canary-7Qw"},
				RenderedParams: map[string]any{"script": "echo Zx9-canary-7Qw"},
				Output:         map[string]any{"stdout": "done"},
				Error:          "exit 1: login failed for Zx9-canary-7Qw",
			}}
		}
	}).Return(nil)
	mockTemporalClient.On("QueryWorkflow", mock.Anything, mock.Anything, mock.Anything, "getDryRunTrace").Return(mockQueryValue, nil)

	resp, err := service.DryRunWorkflow(sc, "test-account", req)
	require.NoError(t, err)
	require.Len(t, resp.Tasks, 1)

	var leaks []string
	findCanary(resp.Tasks, &leaks)
	assert.Empty(t, leaks, "the dry-run trace leaked the credential at: %v", leaks)
	assert.Equal(t, map[string]any{"stdout": "done"}, resp.Tasks[0].Output, "outputs stay readable")
	assert.Equal(t, "exit 1: login failed for ***", resp.Tasks[0].Error)
}
