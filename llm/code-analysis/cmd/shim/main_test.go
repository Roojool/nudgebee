package main

import "testing"

func TestResultExitCode(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   int
	}{
		{name: "structured map", result: map[string]any{"exit_code": float64(7)}, want: 7},
		{name: "legacy JSON string", result: `{"exit_code":124}`, want: 124},
		{name: "success", result: map[string]any{"exit_code": float64(0)}},
		{name: "invalid high exit", result: map[string]any{"exit_code": float64(999)}},
		{name: "unstructured", result: "plain output"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resultExitCode(tc.result); got != tc.want {
				t.Fatalf("resultExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}
