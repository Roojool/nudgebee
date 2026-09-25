package account

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsK8sAgentConnectedEmptyAccount asserts the guard short-circuits before
// touching the database. The predicate itself (and its fail-open policy) is
// tested in relay.TestAgentConnectedFromLookup.
func TestIsK8sAgentConnectedEmptyAccount(t *testing.T) {
	if IsK8sAgentConnected("") {
		t.Error("IsK8sAgentConnected(\"\") = true, want false")
	}
}

// TestAgentNotConnectedErrorContract pins both halves of the contract: errors.Is
// matches the sentinel (observability logs these at WARN), and the messages stay
// byte-identical because other packages still match on the substring.
func TestAgentNotConnectedErrorContract(t *testing.T) {
	cases := []struct {
		err     error
		wantMsg string
	}{
		{agentNotConnectedError{msg: "agent not connected"}, "agent not connected"},
		{agentNotConnectedError{msg: fmt.Sprintf("no agent connected for account %s", "acc-1")}, "no agent connected for account acc-1"},
	}
	for _, c := range cases {
		if !errors.Is(c.err, ErrAgentNotConnected) {
			t.Errorf("errors.Is(%q, ErrAgentNotConnected) = false, want true", c.err)
		}
		if !errors.Is(fmt.Errorf("wrapped: %w", c.err), ErrAgentNotConnected) {
			t.Errorf("wrapped %q does not match ErrAgentNotConnected", c.err)
		}
		if c.err.Error() != c.wantMsg {
			t.Errorf("Error() = %q, want %q", c.err.Error(), c.wantMsg)
		}
	}
	if errors.Is(errors.New("accountId is required"), ErrAgentNotConnected) {
		t.Error("unrelated error matched ErrAgentNotConnected")
	}
}
