package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

// "No change was made" and "the change is already there" are different answers.
// The first is what a run without a proposal earned; only the second may be
// shown when the agent actually checked.
func TestNoOpTerminalAnswerOnlyClaimsPresenceOnAVerdict(t *testing.T) {
	cases := []struct {
		name         string
		basis        string
		wantPresence bool
	}{
		{"specialist checked and said so", "specialist_verdict", true},
		{"nothing was proposed", "no_implementation_instructions", false},
		{"no basis at all", "", false},
		{"a basis we do not know", "some_future_value", false},
	}

	for _, c := range cases {
		resp := map[string]any{"execution_status": "no_op"}
		if c.basis != "" {
			resp["no_op_basis"] = c.basis
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		answer, isNoOp := noOpTerminalAnswer(string(raw))
		if !isNoOp {
			t.Fatalf("%s: expected a no-op answer", c.name)
		}

		var envelope map[string]any
		if err := json.Unmarshal([]byte(answer), &envelope); err != nil {
			t.Fatalf("%s: answer is not an envelope: %v", c.name, err)
		}
		message, _ := envelope["message"].(string)
		claimsPresence := strings.Contains(strings.ToLower(message), "already present in the repository")

		if claimsPresence != c.wantPresence {
			t.Errorf("%s: claims presence = %v, want %v (message: %q)", c.name, claimsPresence, c.wantPresence, message)
		}
		if !c.wantPresence && !strings.Contains(strings.ToLower(message), "not confirmed against the target file") {
			t.Errorf("%s: message should say the values were not confirmed: %q", c.name, message)
		}
	}
}

// The basis has to survive into the envelope, because api-server reads it there
// to decide whether the recommendation may be retired.
func TestNoOpTerminalAnswerPreservesTheBasis(t *testing.T) {
	raw := `{"execution_status":"no_op","no_op_basis":"specialist_verdict"}`
	answer, isNoOp := noOpTerminalAnswer(raw)
	if !isNoOp {
		t.Fatal("expected a no-op answer")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(answer), &envelope); err != nil {
		t.Fatalf("answer is not an envelope: %v", err)
	}
	if got := envelope["no_op_basis"]; got != "specialist_verdict" {
		t.Errorf("basis did not survive into the envelope: got %v", got)
	}
}
