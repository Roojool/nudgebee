package event

import (
	"testing"

	"nudgebee/services/common"
	"nudgebee/services/internal/database/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

// diffEvidence builds the `type: "diff"` evidence block the k8s agents attach
// to a config-change event, with the updated_values shape the revert path reads.
func diffEvidence(updatedValues []any) *models.Json {
	j := models.NewJsonArray([]any{
		map[string]any{"type": "table", "data": map[string]any{"table_name": "*Alert labels*"}},
		map[string]any{
			"type": "diff",
			"data": map[string]any{
				"resource_name":  "deployment/shop/services-server.yaml",
				"old":            "metadata:\n  name: services-server\n",
				"new":            "metadata:\n  name: services-server\n",
				"updated_values": updatedValues,
			},
		},
	})
	return &j
}

func revertEvent(updatedValues []any) models.Event {
	return models.Event{
		Id:               "evt-1",
		SubjectNamespace: strPtr("shop"),
		Evidences:        diffEvidence(updatedValues),
	}
}

var deploymentResource = models.Resource{ResourceId: strPtr("shop/Deployment/services-server")}

// The request must carry the changed field paths, NOT the evidence's
// pre-change manifest: that manifest is snake_case (Hikaru wire compat), and
// replaying it through the agent's dynamic client made the apiserver drop every
// renamed key and reject the remains with "spec.selector: empty selector is
// invalid for deployment".
func TestGetRevertRecommendationRequest_SendsPathsNotManifest(t *testing.T) {
	req, err := getRevertRecommendationRequest(deploymentResource, revertEvent([]any{
		map[string]any{"path": "spec.template.metadata.annotations.rollme", "old": "zde7X", "new": "c4cjS"},
		map[string]any{"path": "spec.template.spec.containers[0].image", "old": "registry/app:old", "new": "registry/app:new"},
	}), false)
	require.NoError(t, err)

	assert.Equal(t, "services-server", req["name"])
	assert.Equal(t, "Deployment", req["kind"])
	assert.Equal(t, strPtr("shop"), req["namespace"])
	assert.Equal(t, []any{
		map[string]any{"path": "spec.template.metadata.annotations.rollme", "old": "zde7X"},
		map[string]any{"path": "spec.template.spec.containers[0].image", "old": "registry/app:old"},
	}, req["revert_paths"])

	// The snake_case manifest still rides along for tenants on the Python agent,
	// which is the only consumer that can read it.
	assert.Contains(t, req, "deployment")
}

// A tenant still on the Python agent gets served by its replace_workload, which
// reads the per-kind manifest and ignores unknown params — so the manifest must
// keep going out even once revert_paths is the shape that matters.
func TestGetRevertRecommendationRequest_KeepsLegacyManifestForPythonAgent(t *testing.T) {
	req, err := getRevertRecommendationRequest(deploymentResource, revertEvent([]any{
		map[string]any{"path": "spec.replicas", "old": float64(2)},
	}), false)
	require.NoError(t, err)
	require.Contains(t, req, "deployment", "per-kind manifest missing: %#v", req)

	// Assert on the marshalled payload, not the Go types: yaml.v2 decodes nested
	// maps as map[interface{}]interface{}, which encoding/json refuses outright.
	// It only reaches the agent because common.MarshalJson is jsoniter. This
	// assertion is the tripwire if that ever changes.
	payload, err := common.MarshalJson(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name": "services-server",
		"namespace": "shop",
		"kind": "Deployment",
		"revert_paths": [{"path": "spec.replicas", "old": 2}],
		"deployment": {"metadata": {"name": "services-server"}}
	}`, string(payload))
}

// Without updated_values the paths can't be built, but the manifest alone still
// lets a Python-agent tenant revert, so the request must not be refused.
func TestGetRevertRecommendationRequest_ManifestOnly(t *testing.T) {
	req, err := getRevertRecommendationRequest(deploymentResource, revertEvent(nil), false)
	require.NoError(t, err)
	assert.NotContains(t, req, "revert_paths")
	assert.Contains(t, req, "deployment")
}

// `old: null` means the change ADDED the field. It has to survive to the agent
// as a null so the agent removes the field rather than writing a null into it.
func TestGetRevertRecommendationRequest_KeepsNullOld(t *testing.T) {
	req, err := getRevertRecommendationRequest(deploymentResource, revertEvent([]any{
		map[string]any{"path": "spec.template.metadata.annotations.rollme", "old": nil, "new": "c4cjS"},
	}), false)
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"path": "spec.template.metadata.annotations.rollme", "old": nil}}, req["revert_paths"])
}

// Previously this returned an empty params map with a nil error, so the agent
// got a bodyless replace_workload and failed with an opaque "name required".
func TestGetRevertRecommendationRequest_NoDiffEvidence(t *testing.T) {
	empty := models.NewJsonArray([]any{map[string]any{"type": "table", "data": map[string]any{}}})
	_, err := getRevertRecommendationRequest(deploymentResource, models.Event{
		Id:               "evt-1",
		SubjectNamespace: strPtr("shop"),
		Evidences:        &empty,
	}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recorded configuration change")
}

func TestGetRevertRecommendationRequest_Rejects(t *testing.T) {
	t.Run("no cloud resource", func(t *testing.T) {
		_, err := getRevertRecommendationRequest(models.Resource{}, revertEvent(nil), false)
		require.Error(t, err)
	})
	t.Run("malformed resource id", func(t *testing.T) {
		_, err := getRevertRecommendationRequest(models.Resource{ResourceId: strPtr("services-server")}, revertEvent(nil), false)
		require.Error(t, err)
	})
	t.Run("nil evidences", func(t *testing.T) {
		_, err := getRevertRecommendationRequest(deploymentResource, models.Event{Id: "evt-1"}, false)
		require.Error(t, err)
	})
	t.Run("diff evidence with neither paths nor manifest", func(t *testing.T) {
		bare := models.NewJsonArray([]any{map[string]any{"type": "diff", "data": map[string]any{}}})
		_, err := getRevertRecommendationRequest(deploymentResource, models.Event{Id: "evt-1", Evidences: &bare}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no recorded configuration change")
	})
	// An OOM alert with five correlated ConfigMap edits attached: with no change selected, the
	// paths of every edit were merged and written onto the workload the alert is about.
	t.Run("several correlated changes and none selected", func(t *testing.T) {
		cm := func(ts string) map[string]any {
			return map[string]any{"type": "diff", "data": map[string]any{
				"resource_name":  "configmap/shop/rulefiles-0.yaml",
				"updated_values": []any{map[string]any{"path": "data.rules", "old": ts, "new": "x"}},
			}}
		}
		several := models.NewJsonArray([]any{cm("06:28"), cm("06:26")})
		_, err := getRevertRecommendationRequest(deploymentResource, models.Event{Id: "evt-1", Evidences: &several}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "select the change to revert")
	})
}

func TestExtractRevertPaths_SkipsMalformedEntries(t *testing.T) {
	paths := extractRevertPaths(revertEvent([]any{
		map[string]any{"path": "spec.replicas", "old": float64(2)},
		map[string]any{"old": "orphan"}, // no path
		"not-an-object",
	}), false)
	assert.Equal(t, []any{map[string]any{"path": "spec.replicas", "old": float64(2)}}, paths)
}

// Undoing a revert is the same operation with the diff read the other way round. One builder serves
// both directions, so the undo path cannot drift from the revert path that runs every day.
func TestGetRevertRecommendationRequest_UndoReappliesTheChange(t *testing.T) {
	event := revertEvent([]any{
		map[string]any{"path": "spec.template.spec.containers[0].image", "old": "app:v1", "new": "app:v2"},
	})

	revert, err := getRevertRecommendationRequest(deploymentResource, event, false)
	require.NoError(t, err)
	assert.Equal(t,
		[]any{map[string]any{"path": "spec.template.spec.containers[0].image", "old": "app:v1"}},
		revert["revert_paths"], "revert writes the value from before the change")

	undo, err := getRevertRecommendationRequest(deploymentResource, event, true)
	require.NoError(t, err)
	assert.Equal(t,
		[]any{map[string]any{"path": "spec.template.spec.containers[0].image", "old": "app:v2"}},
		undo["revert_paths"], "undo writes the value the change introduced")
}

// A field the change ADDED has old=null, so reverting deletes it. Undoing that must put it back
// rather than delete it again — the asymmetry that makes a naive "re-run it" undo wrong.
func TestGetRevertRecommendationRequest_UndoRestoresAnAddedField(t *testing.T) {
	event := revertEvent([]any{
		map[string]any{"path": "spec.template.metadata.annotations.rollme", "old": nil, "new": "c4cjS"},
	})
	undo, err := getRevertRecommendationRequest(deploymentResource, event, true)
	require.NoError(t, err)
	assert.Equal(t,
		[]any{map[string]any{"path": "spec.template.metadata.annotations.rollme", "old": "c4cjS"}},
		undo["revert_paths"])
}

// changedValuesOf feeds the conflict check, so it must read the same diff evidence the revert
// itself is built from — a disagreement between the two would let a conflicting change through.
func TestChangedValuesOf(t *testing.T) {
	e := revertEvent([]any{
		map[string]any{"path": "spec.template.spec.containers[0].image", "old": "v1", "new": "v2"},
		map[string]any{"path": "spec.replicas", "old": float64(3), "new": float64(5)},
	})

	values := changedValuesOf(e)
	assert.Equal(t, "v2", values["spec.template.spec.containers[0].image"], "the value the change LEFT is what a later change is compared against")
	assert.Equal(t, float64(5), values["spec.replicas"])

	assert.Empty(t, changedValuesOf(models.Event{Id: "evt-2"}), "an event with no evidence touches no paths")
}

// Dev records the same config change twice: undo-test-app carries pairs of events sharing a
// timestamp, a path and both values. With `starts_at >=` catching same-instant changes, a twin
// looks exactly like a later change on the same path — and refusing a safe revert is worse than
// the gap that predicate was widened to close. The values are what tell them apart.
func TestSameChangedValue(t *testing.T) {
	assert.True(t, sameChangedValue("busybox:1.36", "busybox:1.36"), "a duplicate record is not a conflict")
	assert.False(t, sameChangedValue("busybox:1.36", "busybox:1.37"), "a different value IS a conflict")

	cmd := []any{"/bin/sh", "-c", "echo boom; exit 1"}
	assert.True(t, sameChangedValue(cmd, []any{"/bin/sh", "-c", "echo boom; exit 1"}))
	assert.False(t, sameChangedValue(cmd, []any{"/bin/sh", "-c", "echo ok"}))

	// nil means the field was absent on that side; absent equals absent, and never equals a value.
	assert.True(t, sameChangedValue(nil, nil))
	assert.False(t, sameChangedValue(nil, "x"))
	assert.False(t, sameChangedValue("x", nil))
}

func TestSameChangeTarget(t *testing.T) {
	resource := "ns/Deployment/api"
	other := "ns/Deployment/worker"
	key := "ns/Deployment/api"

	assert.True(t, sameChangeTarget(
		models.Event{CloudResourceId: &resource},
		models.Event{CloudResourceId: &resource},
	))
	assert.False(t, sameChangeTarget(
		models.Event{CloudResourceId: &resource},
		models.Event{CloudResourceId: &other},
	), "a change on another workload must never be revertible from this event")

	// cloud_resource_id is preferred, but an event recorded before its resource synced has only
	// service_key — the same fallback the resource lookup uses.
	assert.True(t, sameChangeTarget(
		models.Event{ServiceKey: &key},
		models.Event{ServiceKey: &key},
	))
	assert.False(t, sameChangeTarget(models.Event{}, models.Event{}),
		"two events that identify no resource are not therefore the same resource")
}

func TestSameStringValue(t *testing.T) {
	a, b := "x", "x"
	c := "y"
	assert.True(t, sameStringValue(&a, &b))
	assert.False(t, sameStringValue(&a, &c))
	// A nil tenant must not compare equal to anything: it is missing, not a wildcard.
	assert.False(t, sameStringValue(nil, &a))
	assert.False(t, sameStringValue(&a, nil))
	assert.False(t, sameStringValue(nil, nil))
}

func TestHasManifestPayload(t *testing.T) {
	// The builder's fixed fields alone are not a manifest.
	assert.False(t, hasManifestPayload(map[string]any{
		"name": "services-server", "namespace": "shop", "kind": "Deployment",
	}))
	assert.False(t, hasManifestPayload(map[string]any{
		"name": "services-server", "namespace": "shop", "kind": "Deployment",
		"revert_paths": []any{map[string]any{"path": "spec.replicas", "old": 3}},
	}))

	// The per-kind key is whatever the kind lowercases to, so the test is "anything else present".
	for _, kind := range []string{"deployment", "statefulset", "daemonset", "rollout"} {
		assert.True(t, hasManifestPayload(map[string]any{
			"name": "services-server", "namespace": "shop", "kind": "Deployment",
			kind: map[string]any{"spec": map[string]any{}},
		}), kind)
	}

	// A manifest alongside paths is the compatibility shape sent to un-migrated tenants; the
	// path-based check runs for it, so this only has to not crash.
	assert.True(t, hasManifestPayload(map[string]any{
		"name": "services-server", "namespace": "shop", "kind": "Deployment",
		"revert_paths": []any{}, "deployment": map[string]any{},
	}))
}
