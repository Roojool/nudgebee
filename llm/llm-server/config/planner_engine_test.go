package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePlannerEngineConfig(t *testing.T) {
	tests := []struct {
		name          string
		rawEngine     string
		explicitlySet bool
		want          string
		wantErr       bool
		errSubstring  string
	}{
		{
			name:          "unset defaults to auto",
			rawEngine:     "",
			explicitlySet: false,
			want:          "auto",
		},
		{
			name:          "explicit empty defaults to auto",
			rawEngine:     "",
			explicitlySet: true,
			want:          "auto",
		},
		{
			name:          "explicit whitespace defaults to auto",
			rawEngine:     "   ",
			explicitlySet: true,
			want:          "auto",
		},
		{
			name:          "explicit auto",
			rawEngine:     "auto",
			explicitlySet: true,
			want:          "auto",
		},
		{
			name:          "explicit react3",
			rawEngine:     "react3",
			explicitlySet: true,
			want:          "react3",
		},
		{
			name:          "explicit react4",
			rawEngine:     "react4",
			explicitlySet: true,
			want:          "react4",
		},
		{
			name:          "case and whitespace normalized",
			rawEngine:     "  ReAct3 \t",
			explicitlySet: true,
			want:          "react3",
		},
		{
			name:          "typo react33 rejected",
			rawEngine:     "react33",
			explicitlySet: true,
			wantErr:       true,
			errSubstring:  "invalid llm_server_planner_engine \"react33\"",
		},
		{
			name:          "unknown value rejected",
			rawEngine:     "custom_engine",
			explicitlySet: true,
			wantErr:       true,
			errSubstring:  "invalid llm_server_planner_engine \"custom_engine\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePlannerEngineConfig(tt.rawEngine, tt.explicitlySet)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstring)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
