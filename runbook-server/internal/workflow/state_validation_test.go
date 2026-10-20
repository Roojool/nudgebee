package workflow

import (
	"testing"

	"nudgebee/runbook/internal/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// set_state must not copy a credential into workflow_state: the secret already
// lives in Configs, so a step that needs it reads it there.
func TestValidateNoSecretsInState(t *testing.T) {
	stash := model.Task{ID: "stash", SetVars: map[string]any{"pw": "{{ Secrets.token }}"}}
	for _, tc := range []struct {
		name  string
		tasks []model.Task
		want  string // substring of the error; "" means valid
	}{
		{"no state", []model.Task{{ID: "a", Params: map[string]any{"m": "{{ Secrets.token }}"}}}, ""},
		{"clean state", []model.Task{{ID: "a", SetState: map[string]any{"last_run": "{{ Inputs.day }}"}}}, ""},
		{"secret in value", []model.Task{{ID: "a", SetState: map[string]any{"saved": "{{ Secrets.token }}"}}}, "a.saved"},
		{"bracket form", []model.Task{{ID: "a", SetState: map[string]any{"saved": "{{ Secrets['token'] }}"}}}, "a.saved"},
		{"secret in key", []model.Task{{ID: "a", SetState: map[string]any{"run_{{ Secrets.token }}": "x"}}}, "a.run_"},
		{"through a var", []model.Task{stash, {ID: "b", SetState: map[string]any{"saved": "{{ Vars.pw }}"}}}, "b.saved"},
		{"var in key", []model.Task{stash, {ID: "b", SetState: map[string]any{"key_{{ Vars.pw }}": "v"}}}, "b.key_"},
		{"var chain", []model.Task{stash,
			{ID: "b", SetVars: map[string]any{"pw2": "x{{ Vars.pw }}"}},
			{ID: "c", SetState: map[string]any{"saved": "{{ Vars.pw2 }}"}}}, "c.saved"},
		{"through state", []model.Task{
			{ID: "a", SetState: map[string]any{"s1": "{{ Secrets.token }}"}},
			{ID: "b", SetState: map[string]any{"s2": "{{ State.s1 }}"}}}, "b.s2"},
		{"nested in group", []model.Task{{ID: "grp", Type: "core.group", Params: map[string]any{"tasks": []any{
			map[string]any{"id": "inner", "type": "core.print", "set_state": map[string]any{"saved": "{{ Secrets.token }}"}},
		}}}}, "inner.saved"},
		// A token fetched with a password is the author's to keep.
		{"output of a step that used a secret", []model.Task{
			{ID: "login", Params: map[string]any{"password": "{{ Secrets.token }}"}},
			{ID: "b", SetState: map[string]any{"access_token": "{{ Tasks.login.output.token }}"}}}, ""},
		{"self output", []model.Task{
			{ID: "login", Params: map[string]any{"password": "{{ Secrets.token }}"}, SetState: map[string]any{"access_token": "{{ Self.output.token }}"}}}, ""},
		{"clean var", []model.Task{{ID: "a", SetVars: map[string]any{"v": "1"}}, {ID: "b", SetState: map[string]any{"s": "{{ Vars.v }}"}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNoSecretsInState(tc.tasks)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "read it from Secrets")
		})
	}
}

// Only the task whose own write carries the credential is named.
func TestValidateNoSecretsInState_NamesOnlyTheOffendingWrite(t *testing.T) {
	err := ValidateNoSecretsInState([]model.Task{
		{ID: "clean", SetState: map[string]any{"saved": "plain"}},
		{ID: "dirty", SetState: map[string]any{"saved": "{{ Secrets.token }}"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dirty.saved")
	assert.NotContains(t, err.Error(), "clean.saved")
}
