package api

import (
	"strings"
	"testing"

	"nudgebee/services/triage"
)

// A ConfigMap edit and the workload's own rollout share a SubjectKey by design:
// the owning workload is what attaches the edit to the seed at all
// (AssembleTiers requires ek == sk). They also share one aggregation key. The
// display fold keyed on exactly those two, so three distinct changed objects
// collapsed into a single "was deployed / changed · happened 3 times" row and
// the reader could not see that a ConfigMap had changed.
func TestConfigChangeFoldKeepsDistinctObjectsApart(t *testing.T) {
	foldKey := func(subjectKey, agg, subjectName string, isConfigChange bool) string {
		k := subjectKey + "\x00" + agg
		if isConfigChange {
			k += "|" + strings.ToLower(strings.TrimSpace(subjectName))
		}
		return k
	}
	const (
		sk  = "chg-test|chg-test-api"
		agg = "ConfigurationChange/KubernetesResource/Change"
	)

	cmKey := foldKey(sk, agg, "chg-test-config", true)
	deployKey := foldKey(sk, agg, "chg-test-api", true)
	if cmKey == deployKey {
		t.Error("a ConfigMap edit and the workload's own rollout must not fold into one row")
	}

	// Repeat edits to one object still fold — that is what the count is for.
	if foldKey(sk, agg, "chg-test-config", true) != cmKey {
		t.Error("two edits to the same ConfigMap must fold together")
	}

	// Non-change alerts keep the original key: same-subject alerts are meant
	// to collapse into one row with an occurrence count.
	a := foldKey(sk, "pod_oom_killer_enricher", "pod-a", false)
	b := foldKey(sk, "pod_oom_killer_enricher", "pod-b", false)
	if a != b {
		t.Error("non-change alerts on one subject must still fold")
	}
}

// The fold change must not disturb tiering: a config change reaches TierCause
// only when its SubjectKey equals the seed's, which is precisely what the
// owner-based attribution provides.
func TestConfigChangeStillAttachesToTheSeedBySubjectKey(t *testing.T) {
	seed := triage.AlertIdentity{SubjectNamespace: "chg-test", SubjectOwner: "chg-test-api", SubjectType: "pod"}
	cmChange := triage.AlertIdentity{
		ID: "cm-1", SubjectNamespace: "chg-test", SubjectName: "chg-test-config",
		SubjectOwner: "chg-test-api", SubjectType: "configmap", IsConfigChange: true, TsOffsetS: -300,
	}
	if triage.SubjectKey(cmChange) != triage.SubjectKey(seed) {
		t.Fatal("the owning workload is what attaches a ConfigMap edit to the seed; without it the change never reaches the cause tier")
	}
	tiers := triage.AssembleTiers(seed, []triage.AlertIdentity{cmChange}, nil, nil)
	if tiers[cmChange.ID] != triage.TierCause {
		t.Errorf("tier = %q; want cause", tiers[cmChange.ID])
	}
}
