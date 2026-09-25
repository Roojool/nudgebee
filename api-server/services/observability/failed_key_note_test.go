package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An empty Payload has to say why, whatever the cause. The layer already guaranteed that
// for a key a provider cannot answer; these pin the same for a key whose query failed.
//
// It matters because Error is easy to miss and Payload is not: 91 call sites across
// api-server read Payload, 62 check Error. A caller reading only Payload sees an empty
// list and concludes the workload used nothing.

func strp(s string) *string { return &s }

func TestNoteFailedKeys_FailureExplainsItself(t *testing.T) {
	out := OutputMetricQuery{Results: []QueryResult{
		{QueryKey: "memory_usage", Error: strp("datadog rate-limited (429)")},
	}}
	noteFailedKeys(&out)
	assert.Contains(t, out.Results[0].Note, "no data")
	assert.Contains(t, out.Results[0].Note, "429", "the note must carry the cause, not just the fact")
}

// A provider that already explained itself keeps its own wording — the unsupported-key
// note is more useful than a generic failure one.
func TestNoteFailedKeys_DoesNotOverwriteAnExistingNote(t *testing.T) {
	out := OutputMetricQuery{Results: []QueryResult{
		{QueryKey: "container_memory_max_usage_by_pod",
			Error: strp("boom"),
			Note:  "Datadog reports no cgroup high-water mark."},
	}}
	noteFailedKeys(&out)
	assert.Equal(t, "Datadog reports no cgroup high-water mark.", out.Results[0].Note)
}

// A key that simply had no data is NOT a failure, and must not be labelled one.
func TestNoteFailedKeys_LeavesSucceededKeysAlone(t *testing.T) {
	out := OutputMetricQuery{Results: []QueryResult{
		{QueryKey: "cpu_usage", Payload: []Result{{}}},
		{QueryKey: "mem_usage"},
		{QueryKey: "empty_string_error", Error: strp("")},
	}}
	noteFailedKeys(&out)
	for _, r := range out.Results {
		assert.Empty(t, r.Note, "%s should carry no note", r.QueryKey)
	}
}
