package observability

import (
	"nudgebee/services/eventrule"
	"nudgebee/services/eventrule/playbooks"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The auto-discovery loop dedups by configured implementation. Registering two
// differently-configured instances of one type under two names used to collapse
// them into a single run: k8s_pod_cpu_metric_enricher sorts first, marked the
// *podMetricAction type executed, and k8s_pod_memory_metric_enricher was skipped
// for every event — so OOMKill investigations only ever showed CPU charts.
func TestPodMetricEnricherVariantsDedupSeparately(t *testing.T) {
	cpu, found := playbooks.GetAction("k8s_pod_cpu_metric_enricher")
	require.True(t, found)
	mem, found := playbooks.GetAction("k8s_pod_memory_metric_enricher")
	require.True(t, found)

	assert.NotEqual(t, eventrule.ImplKeyOf(cpu), eventrule.ImplKeyOf(mem),
		"cpu and memory pod metric enrichers must dedup separately, or one suppresses the other")
	assert.Equal(t, eventrule.ImplKeyOf(cpu), eventrule.ImplKeyOf(cpu), "dedup key must be stable")
}

// Guards the general case: any registered action name
// that shares a dedup key with another name will only ever run once. That is
// correct for a pure alias (same type, same config) and a bug for a configured
// variant, so a new collision has to be a deliberate decision recorded here.
func TestNoUnintendedActionDedupCollisions(t *testing.T) {
	// name -> name it is a deliberate alias of.
	knownAliases := map[string]string{
		// Legacy name kept because playbooks and event rules in the DB still
		// reference it; identical config, so one run is correct.
		"cloud_metrics": "cloud_list_metrics",
	}

	seen := map[eventrule.ActionImplKey]string{}
	for _, name := range playbooks.ListActions() {
		action, found := playbooks.GetAction(name)
		if !found {
			continue
		}
		key := eventrule.ImplKeyOf(action)
		if other, dup := seen[key]; dup {
			if knownAliases[name] == other || knownAliases[other] == name {
				continue
			}
			t.Errorf("actions %q and %q share a dedup key (%v) — only one will ever auto-run. "+
				"If they are configured variants, implement playbooks.PlaybookActionVariant; "+
				"if they are deliberate aliases, add them to knownAliases.", name, other, key.Typ)
			continue
		}
		seen[key] = name
	}
}
