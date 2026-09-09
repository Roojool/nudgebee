package account

import (
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
