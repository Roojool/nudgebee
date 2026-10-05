package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestResolveCritiqueMode(t *testing.T) {
	tests := []struct {
		name                      string
		raw                       string
		set, feedback, actionable bool
		want                      string
		wantErr                   bool
	}{
		{name: "unset, old flags off", feedback: false, want: CritiqueModeOff},
		{name: "unset, feedback only (the default)", feedback: true, want: CritiqueModeAudit},
		{name: "unset, feedback and actionable", feedback: true, actionable: true, want: CritiqueModeGuided},
		{name: "explicit empty falls back to old flags", raw: " ", set: true, feedback: true, actionable: true, want: CritiqueModeGuided},
		{name: "explicit continuation wins over old flags", raw: "Continuation", set: true, want: CritiqueModeContinuation},
		{name: "explicit off wins over old flags", raw: "off", set: true, feedback: true, actionable: true, want: CritiqueModeOff},
		{name: "unknown value", raw: "strict", set: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCritiqueMode(tt.raw, tt.set, tt.feedback, tt.actionable)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// LLM_CRITIQUE_MODE must reach the struct. viper.Unmarshal only maps env vars for
// keys it knows, so without a registered default the variable was silently
// ignored and every deployment stayed on the older flags (found on a local run).
func TestCritiqueModeEnvReachesConfig(t *testing.T) {
	t.Setenv("LLM_CRITIQUE_MODE", "continuation")
	var cfg appConfig
	require.NoError(t, viper.Unmarshal(&cfg))
	require.Equal(t, CritiqueModeContinuation, cfg.CritiqueMode)
}
