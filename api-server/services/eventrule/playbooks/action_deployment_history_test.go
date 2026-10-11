package playbooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"nudgebee/services/internal/testenv"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestDeployment(t *testing.T) {
	testenv.RequireEnv(t, testenv.Tenant, testenv.Account)
	deploymentAction := deploymentHistoryAction{}

	// Parse event start time
	eventStartTime, _ := time.Parse(time.RFC3339, "2025-11-20T14:35:30Z")

	defaultPlaybookActionContext := NewPlaybookActionContext(os.Getenv("TEST_TENANT"), os.Getenv("TEST_ACCOUNT"), slog.Default(), PlaybookEvent{
		Name:        "TestDeploymentAlert",
		SubjectName: "shipment-tracking-service-production-eu-gcp",
		Labels:      map[string]string{"namespace": "p44-production-eu"},
		Annotations: map[string]string{},
		StartedAt:   &eventStartTime,
	})

	response, err := deploymentAction.Execute(defaultPlaybookActionContext, map[string]any{
		"service_name": "shipment-tracking-service-production-eu-gcp",
	})

	assert.NotNil(t, response)
	assert.Nil(t, err)

	jsonBytes, _ := json.Marshal(response)
	fmt.Println(string(jsonBytes))
}

// evidence shapes below mirror real rows: a diff evidence whose data.updated_values
// entries are {path, old, new, report}, old being null when the change added the field.
func diffEvidence(updatedValues []any) []map[string]interface{} {
	return []map[string]interface{}{
		{"type": "json", "data": "raw kubewatch payload"}, // non-diff block must be skipped
		{"type": "diff", "data": map[string]interface{}{
			"old":            "apiVersion: apps/v1\nkind: Deployment\n...",
			"new":            "apiVersion: apps/v1\nkind: Deployment\n...",
			"updated_values": updatedValues,
		}},
	}
}

func TestExtractChangedFields_ReturnsPathsWithoutManifests(t *testing.T) {
	fields, omitted, truncated := extractChangedFields(diffEvidence([]any{
		map[string]interface{}{
			"path": "spec.template.spec.containers[0].image",
			"old":  "registry/app:v1",
			"new":  "registry/app:v2",
		},
		map[string]interface{}{"path": "spec.replicas", "old": float64(3), "new": float64(5)},
	}))

	assert.Equal(t, 0, omitted)
	assert.False(t, truncated)
	assert.Len(t, fields, 2)

	// The two changes must stay distinguishable — this is the whole point of the
	// field: identical titles are why the wrong change gets blamed.
	assert.Equal(t, "spec.template.spec.containers[0].image", fields[0].Path)
	assert.Equal(t, "registry/app:v1", *fields[0].Old)
	assert.Equal(t, "registry/app:v2", *fields[0].New)
	assert.Equal(t, "spec.replicas", fields[1].Path)
	assert.Equal(t, "3", *fields[1].Old)
	assert.Equal(t, "5", *fields[1].New)
}

func TestExtractChangedFields_NullOldMeansFieldWasAdded(t *testing.T) {
	fields, _, _ := extractChangedFields(diffEvidence([]any{
		map[string]interface{}{
			"path": "spec.template.spec.containers[0].env[3]",
			"old":  nil,
			"new":  map[string]interface{}{"name": "GOMEMLIMIT", "value": "921MiB"},
		},
	}))

	assert.Len(t, fields, 1)
	// nil, not "" — the revert path reads a null old as "delete this field", and an
	// empty string would tell it to write an empty value instead.
	assert.Nil(t, fields[0].Old)
	assert.JSONEq(t, `{"name":"GOMEMLIMIT","value":"921MiB"}`, *fields[0].New)
}

func TestExtractChangedFields_CapsFieldsAndValues(t *testing.T) {
	var updatedValues []any
	for i := 0; i < maxChangedFieldsPerDeployment+8; i++ {
		updatedValues = append(updatedValues, map[string]interface{}{
			"path": fmt.Sprintf("spec.template.spec.containers[0].env[%d].value", i),
			"old":  "before",
			"new":  "after",
		})
	}
	fields, omitted, truncated := extractChangedFields(diffEvidence(updatedValues))
	assert.Len(t, fields, maxChangedFieldsPerDeployment)
	assert.Equal(t, 8, omitted, "dropped fields must be counted, not silently lost")
	assert.False(t, truncated)

	// Multi-byte values are ordinary in annotations and env vars. This one is exactly at the limit
	// in CHARACTERS while being twice it in bytes: it must come back whole and unflagged. The first
	// version of this test asserted the opposite and passed, because the code reported truncation
	// from the byte length while the rune loop had removed nothing.
	atLimit := strings.Repeat("é", maxChangedValueChars)
	fields, _, truncated = extractChangedFields(diffEvidence([]any{
		map[string]interface{}{"path": "spec.template.metadata.annotations.note", "old": "", "new": atLimit},
	}))
	assert.Len(t, fields, 1)
	assert.Equal(t, atLimit, *fields[0].New, "a value within the character limit must not be cut")
	assert.False(t, truncated, "nothing was removed, so nothing should claim it was")

	// Genuinely over the limit: cut, flagged, and still valid UTF-8 rather than a split character.
	overLimit := strings.Repeat("é", maxChangedValueChars+40)
	fields, _, truncated = extractChangedFields(diffEvidence([]any{
		map[string]interface{}{"path": "spec.template.metadata.annotations.note", "old": "", "new": overLimit},
	}))
	assert.Len(t, fields, 1)
	assert.True(t, truncated)
	assert.True(t, utf8.ValidString(*fields[0].New), "a truncated value must still be valid UTF-8")
	assert.Equal(t, maxChangedValueChars, utf8.RuneCountInString(*fields[0].New), "cut at the character limit, not the byte limit")

	long := make([]byte, maxChangedValueChars+50)
	for i := range long {
		long[i] = 'x'
	}
	fields, _, truncated = extractChangedFields(diffEvidence([]any{
		map[string]interface{}{"path": "spec.template.spec.containers[0].command", "old": "sh", "new": string(long)},
	}))
	assert.Len(t, fields, 1)
	assert.Len(t, *fields[0].New, maxChangedValueChars)
	assert.True(t, truncated, "a shortened value must be flagged so the UI never presents it as complete")
}

func TestExtractChangedFields_ToleratesMalformedEvidence(t *testing.T) {
	for name, evidences := range map[string][]map[string]interface{}{
		"no evidences":       nil,
		"no diff block":      {{"type": "json", "data": "x"}},
		"data not an object": {{"type": "diff", "data": "not-a-map"}},
		"no updated_values":  {{"type": "diff", "data": map[string]interface{}{"old": "a", "new": "b"}}},
		"entry not an object": {{"type": "diff", "data": map[string]interface{}{
			"updated_values": []any{"not-a-map"},
		}}},
		"entry without path": {{"type": "diff", "data": map[string]interface{}{
			"updated_values": []any{map[string]interface{}{"old": "a", "new": "b"}},
		}}},
	} {
		fields, omitted, truncated := extractChangedFields(evidences)
		assert.Empty(t, fields, name)
		assert.Zero(t, omitted, name)
		assert.False(t, truncated, name)
	}
}
