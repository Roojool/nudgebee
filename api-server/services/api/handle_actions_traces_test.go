package api

import (
	"testing"

	"nudgebee/services/observability"

	"github.com/stretchr/testify/assert"
)

// A bare span array can carry neither the empty-result diagnosis nor the executed query and
// provider, so either flag has to select the envelope. Gating on ValidateRequest alone made
// IncludeExecutedQuery silently useless over HTTP whenever validation was disabled — an empty
// trace result would go back to being unattributable (#37406).
func TestWantsTracesEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		request  observability.TracesV3Request
		expected bool
	}{
		{
			name:     "neither flag keeps the bare span array",
			request:  observability.TracesV3Request{},
			expected: false,
		},
		{
			name:     "validation alone needs the envelope for the suggestion",
			request:  observability.TracesV3Request{ValidateRequest: true},
			expected: true,
		},
		{
			name:     "the executed query alone needs the envelope",
			request:  observability.TracesV3Request{IncludeExecutedQuery: true},
			expected: true,
		},
		{
			name:     "both flags together",
			request:  observability.TracesV3Request{ValidateRequest: true, IncludeExecutedQuery: true},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, wantsTracesEnvelope(tt.request))
		})
	}
}
