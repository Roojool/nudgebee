package integration_test

import (
	"encoding/json"
	"time"

	"nudgebee/runbook/internal/model"
)

// liveCanary is the credential value these tests store and then hunt for.
const liveCanary = "Zx9-canary-7Qw-live"

// TestSecretsStayOutOfInputsAndErrors is the observed-behaviour check for
// #39066: store a real credential, run a real automation that resolves it, then
// read the execution details the UI gets.
//
// Inputs and error messages must never carry the value. Outputs are the
// author's: a step that prints a credential shows it, by the author's choice.
func (s *IntegrationTestSuite) TestSecretsStayOutOfInputsAndErrors() {
	s.createConfig("DB_PASSWORD", liveCanary, string(model.ConfigTypeSecret), nil, nil)
	defer s.deleteConfig("DB_PASSWORD")

	wfDef := model.Workflow{
		Name: "test-secret-canary",
		Definition: model.WorkflowDefinition{
			Triggers: []model.Trigger{{Type: model.WorkflowTriggerManual}},
			Tasks: []model.Task{
				{
					ID:      "stash",
					Type:    "core.print",
					Params:  map[string]any{"message": "stashing"},
					SetVars: map[string]any{"pw": "{{ Secrets.DB_PASSWORD }}"},
				},
				{
					// Names no credential of its own — only a var an earlier step wrote.
					ID:        "useIt",
					Type:      "core.print",
					DependsOn: []string{"stash"},
					Params:    map[string]any{"message": "value is {{ Vars.pw }}"},
				},
				{
					// Fails with the request URL — api_key included — in its error.
					ID:            "callIt",
					Type:          "integrations.http",
					DependsOn:     []string{"stash"},
					Params:        map[string]any{"method": "GET", "url": "https://nonexistent.invalid/data?api_key={{ Secrets.DB_PASSWORD }}"},
					FailurePolicy: &model.FailurePolicy{Action: "continue"},
				},
			},
		},
	}

	createdWf, _, err := s.createAndActivateWorkflow(wfDef)
	s.Require().NoError(err)
	defer s.deleteWorkflow(createdWf.ID, true)

	runID, err := s.executeWorkflow(createdWf.ID, nil)
	s.Require().NoError(err)

	// Temporal's visibility index is eventually consistent; poll until visible.
	s.Require().Eventually(func() bool {
		_, err := s.resolveTemporalWorkflowID(s.T().Context(), createdWf.ID, runID)
		return err == nil
	}, 30*time.Second, 250*time.Millisecond, "run never became visible")

	details := s.waitForWorkflowCompletion(createdWf.ID, runID)
	s.Require().Equal(model.WorkflowExecutionStatusCompleted, details.Status,
		"the automation must still run — redaction that breaks execution is not a fix")
	s.Assert().NotContains(details.Error, liveCanary)

	for _, task := range details.Tasks {
		surfaces, err := json.Marshal(map[string]any{
			"input":           task.Input,
			"rendered_params": task.RenderedParams,
			"error":           task.Error,
		})
		s.Require().NoError(err)
		s.Assert().NotContains(string(surfaces), liveCanary, "task %s leaked the credential", task.ID)
		s.T().Logf("%s: error=%q", task.ID, task.Error)
	}
}

// TestSetStateWithASecretIsRejected: the secret already lives in Configs, so
// copying it into workflow_state is refused when the automation is saved.
func (s *IntegrationTestSuite) TestSetStateWithASecretIsRejected() {
	_, _, err := s.createAndActivateWorkflow(model.Workflow{
		Name: "test-secret-in-state",
		Definition: model.WorkflowDefinition{
			Triggers: []model.Trigger{{Type: model.WorkflowTriggerManual}},
			Tasks: []model.Task{{
				ID:       "stash",
				Type:     "core.print",
				Params:   map[string]any{"message": "stashing"},
				SetState: map[string]any{"saved_pw": "{{ Secrets.DB_PASSWORD }}"},
			}},
		},
	})
	s.Require().Error(err)
	s.Assert().Contains(err.Error(), "set_state cannot store a credential")
}
