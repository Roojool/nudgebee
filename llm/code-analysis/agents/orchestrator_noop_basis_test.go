package agents

import (
	"strings"
	"testing"
)

// A no-op means one of two things, and they are not interchangeable. Either the
// specialist read the file and said no change is needed, or the run ended with
// nothing to act on and never formed a view. Only the first is a finding.
func TestResolveNoOpBasis(t *testing.T) {
	cases := []struct {
		name       string
		resultData map[string]any
		factsData  map[string]any
		want       string
	}{
		{
			name:       "the specialist recorded its own basis and it wins",
			resultData: map[string]any{NoOpBasisKey: NoOpBasisNoInstructions},
			factsData:  map[string]any{"requires_fix_stated": true},
			want:       NoOpBasisNoInstructions,
		},
		{
			name:       "a stated requires_fix is a verdict about the file",
			resultData: map[string]any{},
			factsData:  map[string]any{"requires_fix_stated": true},
			want:       NoOpBasisSpecialistVerdict,
		},
		{
			name:       "an unstated requires_fix is silence, not agreement",
			resultData: map[string]any{},
			factsData:  map[string]any{},
			want:       NoOpBasisNoInstructions,
		},
		{
			name:       "an explicitly false stated flag is also silence",
			resultData: map[string]any{},
			factsData:  map[string]any{"requires_fix_stated": false},
			want:       NoOpBasisNoInstructions,
		},
	}

	for _, c := range cases {
		if got := resolveNoOpBasis(c.resultData, c.factsData); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The reason reaches a human and is stored on the resolution, so it must not
// assert something the run never checked.
func TestNoOpReasonOnlyClaimsPresenceOnAVerdict(t *testing.T) {
	verdict := strings.ToLower(noOpReason(NoOpBasisSpecialistVerdict))
	if !strings.Contains(verdict, "already present") {
		t.Errorf("a verdict should say the change is already present: %q", verdict)
	}

	for _, basis := range []string{NoOpBasisNoInstructions, "", "something_new"} {
		reason := strings.ToLower(noOpReason(basis))
		if strings.Contains(reason, "the specialist determined") {
			t.Errorf("basis %q claims a determination nobody made: %q", basis, reason)
		}
		if !strings.Contains(reason, "not confirmed against the target file") {
			t.Errorf("basis %q should say the values were not confirmed: %q", basis, reason)
		}
	}
}
