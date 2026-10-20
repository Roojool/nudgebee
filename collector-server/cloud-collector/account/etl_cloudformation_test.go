package account

import (
	"encoding/json"
	"testing"

	"nudgebee/collector/cloud/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecodePriorStackRows covers the presence-vs-emptiness rule at the point the
// stored value re-enters Go. Collapsing "absent" and "empty" here is the bug that
// would put a zero-resource stack back on the CLI every graph build forever.
func TestDecodePriorStackRows(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	rows := []priorStackRow{
		{ResourceID: "stack-present", CapturedAt: "T1", Summaries: json.RawMessage(`[{"LogicalResourceId":"B"}]`)},
		{ResourceID: "stack-empty", CapturedAt: "T2", Summaries: json.RawMessage(`[]`)},
		{ResourceID: "stack-sql-null", CapturedAt: "", Summaries: nil},
		{ResourceID: "stack-json-null", CapturedAt: "T3", Summaries: json.RawMessage(`null`)},
		{ResourceID: "stack-not-an-array", CapturedAt: "T4", Summaries: json.RawMessage(`{"nope":1}`)},
	}

	prior := decodePriorStackRows(ctx, "acct", rows)

	present, ok := prior["stack-present"]
	require.True(t, ok)
	assert.Equal(t, "T1", present.CapturedAt)
	assert.JSONEq(t, `[{"LogicalResourceId":"B"}]`, string(present.Summaries))

	// An empty array is a real answer the writer stood behind, so it must survive
	// as a hit rather than being dropped.
	empty, ok := prior["stack-empty"]
	require.True(t, ok, "an empty list is a real answer, not an absent one")
	assert.Equal(t, "T2", empty.CapturedAt)
	assert.JSONEq(t, `[]`, string(empty.Summaries))

	// A SQL NULL means the key was never written: refetch.
	assert.NotContains(t, prior, "stack-sql-null")
	// The writer's contract is "omit the key or write an array", so these two are
	// evidence it misbehaved and are not trusted.
	assert.NotContains(t, prior, "stack-json-null")
	assert.NotContains(t, prior, "stack-not-an-array")
}

func TestDecodePriorStackRowsEmptyInput(t *testing.T) {
	prior := decodePriorStackRows(security.NewRequestContextForSuperAdmin(), "acct", nil)
	assert.Empty(t, prior, "no rows means fetch everything, which is correct if slower")
}
