package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPrimarySubjectName covers the reduction of the extractor agent's answer to the
// one name that becomes events.subject_name. The agent may name several services
// (most relevant first); subject_name is a single column that MatchWorkloadAndEnrich
// looks up, so only the primary one may land there.
func TestPrimarySubjectName(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   string
	}{
		// The normal case, and every answer that predates multi-select: no comma,
		// returned untouched.
		{"single service passes through", "cart-service", "cart-service"},
		{"two services take the first", "cart-service, checkout-api", "cart-service"},
		{"three services take the first", "cart-service, checkout-api, Payment-Gateway", "cart-service"},
		{"separator without a space", "cart-service,checkout-api", "cart-service"},
		{"ragged whitespace around the primary", " cart-service ,checkout-api", "cart-service"},
		{"a lone name is trimmed too, not just a split one", "  cart-service  ", "cart-service"},
		{"trailing comma", "cart-service,", "cart-service"},
		{"empty stays empty", "", ""},
		// A service name containing no comma is safe; names with commas are not
		// possible here because every token was validated against k8s_workloads
		// names by the planner.
		{"hyphenated names are not split", "victoria-kube-state-metrics", "victoria-kube-state-metrics"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, primarySubjectName(tt.answer))
		})
	}
}

// TestLooksLikePlausibleSubject covers the guard that rejects an extractor answer
// that isn't really a service name — observed in prod: an expired Google AI
// CachedContent slot's 403 landing as response text instead of an error.
func TestLooksLikePlausibleSubject(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   bool
	}{
		{"single service", "cart-service", true},
		{"hyphenated name", "victoria-kube-state-metrics", true},
		// The multi-match separator (", ") itself contains a space — must not be
		// mistaken for an implausible answer.
		{"multi-match answer with underscores", "my_container, another_one", true},
		{"multi-match answer, three services", "cart-service, checkout-api, Payment-Gateway", true},
		{"the real prod failure: a stream error landing as content", "error in stream mode: Error 403, Message: CachedContent not found (or permission denied), Status: PERMISSION_DENIED, Details: []", false},
		{"a single token with a space is prose, not a name", "not a real service", false},
		{"a colon anywhere rejects the whole answer", "cart-service, Status: 403", false},
		{"brackets reject the whole answer", "cart-service, Details: []", false},
		// A trailing comma is a stray separator, not a second name — same shape
		// primarySubjectName already tolerates — so it must not itself be implausible.
		{"trailing comma is tolerated, same as primarySubjectName", "cart-service,", true},
		{"trailing comma-space is tolerated", "cart-service, ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikePlausibleSubject(tt.answer))
		})
	}
}
