package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	commonapi "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowapi "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
	"nudgebee/runbook/internal/model"
	"nudgebee/runbook/internal/tasks"
	"nudgebee/runbook/services/security"
)

// newCancelTestService wires a service over a mock Temporal client and store,
// with ListWorkflow answering both ResolveTemporalWorkflowID and the
// post-cancel status sync with an execution in `status`.
func newCancelTestService(status enums.WorkflowExecutionStatus) (*Service, *MockTemporalClient, *MockWorkflowStore) {
	mockTemporalClient := &MockTemporalClient{}
	mockDataConverter := converter.GetDefaultDataConverter()
	mockStore := new(MockWorkflowStore)
	mockConfigService := new(MockConfigService)
	executor := &WorkflowExecutor{temporalClient: mockTemporalClient, workflowStore: mockStore, dataConverter: mockDataConverter}
	service := NewService(mockTemporalClient, mockStore, mockDataConverter, tasks.NewInitializedTaskRegistry(), executor, mockConfigService)

	mockTemporalClient.On("ListWorkflow", mock.Anything, mock.Anything).Return(&workflowservice.ListWorkflowExecutionsResponse{
		Executions: []*workflowapi.WorkflowExecutionInfo{
			{
				Execution: &commonapi.WorkflowExecution{WorkflowId: "temporal-wf-id", RunId: "run-1"},
				Status:    status,
			},
		},
	}, nil)
	mockStore.On("SetLastExecutionStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	return service, mockTemporalClient, mockStore
}

func describeWith(status enums.WorkflowExecutionStatus) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowapi.WorkflowExecutionInfo{
			Execution: &commonapi.WorkflowExecution{WorkflowId: "temporal-wf-id", RunId: "run-1"},
			Status:    status,
		},
	}
}

// A run that honours the cancel request stops on its own — no terminate — and
// the workflow row is reconciled to the status Temporal reports.
func TestCancelWorkflowExecutionGracefulStop(t *testing.T) {
	service, temporalClient, store := newCancelTestService(enums.WORKFLOW_EXECUTION_STATUS_CANCELED)
	temporalClient.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWith(enums.WORKFLOW_EXECUTION_STATUS_CANCELED), nil)

	sc := security.NewRequestContextForTenantAccountAdmin("test-tenant", "test-user", []string{"test-account"})
	assert.NoError(t, service.CancelWorkflowExecution(sc, "test-account", "wf-1", "run-1"))

	assert.Equal(t, []string{"run-1"}, temporalClient.canceled, "cancel must be requested first")
	assert.Empty(t, temporalClient.terminated, "a run that stopped on its own must not be terminated")
	store.AssertCalled(t, "SetLastExecutionStatus", mock.Anything, "test-tenant", "test-account", "wf-1",
		model.WorkflowExecutionStatusCanceled, mock.Anything, mock.Anything, mock.Anything)
}

// #37544: cancelling a run that never observes the cancel request used to leave
// the automation reporting "Running" for good. The cancel now escalates to a
// terminate and writes the terminal status to the workflow row.
func TestCancelWorkflowExecutionTerminatesUnresponsiveRun(t *testing.T) {
	service, temporalClient, store := newCancelTestService(enums.WORKFLOW_EXECUTION_STATUS_TERMINATED)
	temporalClient.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWith(enums.WORKFLOW_EXECUTION_STATUS_RUNNING), nil)

	sc := security.NewRequestContextForTenantAccountAdmin("test-tenant", "test-user", []string{"test-account"})
	start := time.Now()
	assert.NoError(t, service.CancelWorkflowExecution(sc, "test-account", "wf-1", "run-1"))

	assert.Equal(t, []string{"run-1"}, temporalClient.canceled)
	assert.Equal(t, []string{"run-1"}, temporalClient.terminated, "a run still going after the settle window must be terminated")
	assert.Less(t, time.Since(start), 30*time.Second, "the settle window must stay inside one UI click")
	store.AssertCalled(t, "SetLastExecutionStatus", mock.Anything, "test-tenant", "test-account", "wf-1",
		model.WorkflowExecutionStatusTerminated, mock.Anything, mock.Anything, mock.Anything)
}

// A second run of the same automation still going keeps the row at RUNNING:
// the sync reads the latest execution rather than assuming the canceled one.
func TestCancelWorkflowExecutionLeavesConcurrentRunAlone(t *testing.T) {
	service, temporalClient, store := newCancelTestService(enums.WORKFLOW_EXECUTION_STATUS_RUNNING)
	temporalClient.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWith(enums.WORKFLOW_EXECUTION_STATUS_CANCELED), nil)

	sc := security.NewRequestContextForTenantAccountAdmin("test-tenant", "test-user", []string{"test-account"})
	assert.NoError(t, service.CancelWorkflowExecution(sc, "test-account", "wf-1", "run-1"))

	store.AssertNotCalled(t, "SetLastExecutionStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// A terminate that fails — most often because the run closed itself in the
// window after the last poll — must not skip the row reconciliation: that run's
// status is exactly the one the row is still missing. The error is still
// reported, so a terminate that failed for a real reason is not swallowed.
func TestCancelWorkflowExecutionSyncsRowEvenWhenTerminateFails(t *testing.T) {
	service, temporalClient, store := newCancelTestService(enums.WORKFLOW_EXECUTION_STATUS_CANCELED)
	temporalClient.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWith(enums.WORKFLOW_EXECUTION_STATUS_RUNNING), nil)
	temporalClient.terminateErr = errors.New("workflow execution already completed")

	sc := security.NewRequestContextForTenantAccountAdmin("test-tenant", "test-user", []string{"test-account"})
	err := service.CancelWorkflowExecution(sc, "test-account", "wf-1", "run-1")

	assert.Error(t, err)
	store.AssertCalled(t, "SetLastExecutionStatus", mock.Anything, "test-tenant", "test-account", "wf-1",
		model.WorkflowExecutionStatusCanceled, mock.Anything, mock.Anything, mock.Anything)
}

// The click can be gone before the run is: a browser that navigates away cancels
// the request context. Stopping the run and reconciling the row must survive
// that — issuing either on a canceled context fails on the spot and leaves the
// automation on "Running", which is the failure #37544 is about.
func TestCancelWorkflowExecutionSurvivesRequestContextCancellation(t *testing.T) {
	service, temporalClient, store := newCancelTestService(enums.WORKFLOW_EXECUTION_STATUS_TERMINATED)
	temporalClient.On("DescribeWorkflowExecution", mock.Anything, mock.Anything, mock.Anything).
		Return(describeWith(enums.WORKFLOW_EXECUTION_STATUS_RUNNING), nil)

	gone, abandon := context.WithCancel(context.Background())
	abandon()
	sc := security.NewRequestContextForTenantAccountAdmin("test-tenant", "test-user", []string{"test-account"}).WithContext(gone)

	assert.NoError(t, service.CancelWorkflowExecution(sc, "test-account", "wf-1", "run-1"))

	assert.Equal(t, []string{"run-1"}, temporalClient.terminated, "an abandoned request must still stop the run")
	store.AssertCalled(t, "SetLastExecutionStatus", mock.Anything, "test-tenant", "test-account", "wf-1",
		model.WorkflowExecutionStatusTerminated, mock.Anything, mock.Anything, mock.Anything)
}
