package adapter

import (
	"strings"
	"testing"

	"nudgebee/services/internal/database/models"
)

// A workload row carries its name and type on the row itself; only pod-shaped
// rows put the workload under the controller keys. Reading the controller keys
// alone queued a task with a null name, which the agent refused.
func TestAbandonedScaleParamsFallsBackToTheResourceRow(t *testing.T) {
	name, kind := "orders-worker", "Deployment"
	params, err := abandonedScaleParams(models.Resource{
		Name: &name,
		Type: &kind,
		Meta: jsonFixture(t, map[string]any{"namespace": "payments", "config": map[string]any{"labels": map[string]any{}}}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for key, want := range map[string]any{"name": "orders-worker", "namespace": "payments", "kind": "Deployment", "replica_count": 0} {
		if params[key] != want {
			t.Fatalf("%s: got %v, want %v", key, params[key], want)
		}
	}
}

func TestAbandonedScaleParamsPrefersTheControllerKeys(t *testing.T) {
	name, kind := "orders-worker-7d9f", "ReplicaSet"
	params, err := abandonedScaleParams(models.Resource{
		Name: &name,
		Type: &kind,
		Meta: jsonFixture(t, map[string]any{"namespace": "payments", "controller": "orders-worker", "controllerKind": "Deployment"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params["name"] != "orders-worker" || params["kind"] != "Deployment" {
		t.Fatalf("got name=%v kind=%v", params["name"], params["kind"])
	}
}

func TestAbandonedScaleParamsRefusesAnUnnamedTarget(t *testing.T) {
	kind := "Deployment"
	for label, resource := range map[string]models.Resource{
		"no name":      {Type: &kind, Meta: jsonFixture(t, map[string]any{"namespace": "payments"})},
		"no namespace": {Name: &kind, Type: &kind, Meta: jsonFixture(t, map[string]any{})},
	} {
		t.Run(label, func(t *testing.T) {
			if _, err := abandonedScaleParams(resource); err == nil || !strings.Contains(err.Error(), "required") {
				t.Fatalf("expected a refusal, got %v", err)
			}
		})
	}
}
