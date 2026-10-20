package agents

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A branch with no commits must stay distinguishable from other push failures.
//
// Pushing an empty branch used to produce
// "GraphQL: Head sha can't be blank, Base sha can't be blank, No commits" from
// the provider — an opaque API error. Recurring since at least April. The caller
// keys off this sentinel to report that the fix was applied but never reached the
// branch, so a wrapped error must still be recognisable.
func TestErrNoCommitsToPublish_IsDistinguishable(t *testing.T) {
	require.Error(t, errNoCommitsToPublish)

	assert.True(t, errors.Is(errNoCommitsToPublish, errNoCommitsToPublish))
	assert.True(t, errors.Is(fmt.Errorf("createPullRequest: %w", errNoCommitsToPublish), errNoCommitsToPublish),
		"the sentinel must survive wrapping — the caller matches on it to report a no-op")

	assert.False(t, errors.Is(errors.New("failed to push branch"), errNoCommitsToPublish),
		"an unrelated failure must not be mistaken for a no-op")
}
