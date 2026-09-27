package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExecutionTarget(t *testing.T) {
	for _, provider := range []string{"kubernetes", "aws", "gcp", "azure"} {
		target, err := ParseExecutionTarget(NBToolCallRequest{Arguments: map[string]any{
			"target": map[string]any{"provider": provider, "name": "prod"},
		}})
		require.NoError(t, err)
		require.Equal(t, &ExecutionTarget{Provider: provider, Name: "prod"}, target)
	}
	target, err := ParseExecutionTarget(NBToolCallRequest{Command: `{"command":"inspect","target":{"provider":"GCP","name":" prod "}}`})
	require.NoError(t, err)
	require.Equal(t, &ExecutionTarget{Provider: "gcp", Name: "prod"}, target)
	target, err = ParseExecutionTarget(NBToolCallRequest{Command: "inspect"})
	require.NoError(t, err)
	require.Nil(t, target)
	args := map[string]any{}
	target, err = ParseExecutionTarget(NBToolCallRequest{Command: `{"target":{"provider":"aws","name":"prod"}}`, Arguments: args})
	require.NoError(t, err)
	require.Equal(t, "prod", target.Name)
	require.Empty(t, args, "parsing must not mutate the caller's arguments")
	args = map[string]any{"max_iterations": 5}
	target, err = ParseExecutionTarget(NBToolCallRequest{Command: `{"target":{"provider":"aws","name":"prod"}}`, Arguments: args})
	require.NoError(t, err)
	require.Equal(t, "prod", target.Name, "other structured arguments must not hide the JSON target")
	require.Equal(t, map[string]any{"max_iterations": 5}, args)
	for _, invalid := range []any{"prod", map[string]any{"provider": "gcp"}, map[string]any{"provider": "unknown", "name": "prod"}} {
		_, err := ParseExecutionTarget(NBToolCallRequest{Arguments: map[string]any{"target": invalid}})
		require.Error(t, err)
	}
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing provider", map[string]any{"name": "prod"}, "missing the provider field"},
		{"wrong provider type", map[string]any{"provider": 42, "name": "prod"}, "provider must be a string"},
		{"empty provider", map[string]any{"provider": "  ", "name": "prod"}, "provider cannot be empty"},
		{"missing name", map[string]any{"provider": "aws"}, "missing the name field"},
		{"wrong name type", map[string]any{"provider": "aws", "name": 42}, "name must be a string"},
		{"empty name", map[string]any{"provider": "aws", "name": "  "}, "name cannot be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseExecutionTarget(NBToolCallRequest{Arguments: map[string]any{"target": tc.raw}})
			require.ErrorContains(t, err, tc.want)
		})
	}
}
