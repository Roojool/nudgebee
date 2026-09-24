package observability

import (
	"log/slog"
	"testing"

	"nudgebee/services/eventrule/playbooks"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func overcommitCtx(aggKey string) playbooks.PlaybookActionContext {
	return playbooks.NewPlaybookActionContext("t", "a", slog.Default(), playbooks.PlaybookEvent{
		AggregationKey: aggKey,
	})
}

// TestOvercommitCanAutoExecute — a CPU over-commitment alert must get the CPU
// cards and not the memory ones. Both alerts fire together on a squeezed
// cluster, so without the per-resource gate every such event would carry four
// cards, two of them answering a question nobody asked.
func TestOvercommitCanAutoExecute(t *testing.T) {
	cpu := &overcommitAction{resource: "cpu"}
	mem := &overcommitAction{resource: "memory"}

	assert.True(t, cpu.CanAutoExecute(overcommitCtx("KubeCPUOvercommit")))
	assert.False(t, mem.CanAutoExecute(overcommitCtx("KubeCPUOvercommit")))

	assert.True(t, mem.CanAutoExecute(overcommitCtx("KubeMemoryOvercommit")))
	assert.False(t, cpu.CanAutoExecute(overcommitCtx("KubeMemoryOvercommit")))

	// Quota variants are the same statement scoped to a namespace's quota.
	assert.True(t, cpu.CanAutoExecute(overcommitCtx("KubeCPUQuotaOvercommit")))
	assert.True(t, mem.CanAutoExecute(overcommitCtx("KubeMemoryQuotaOvercommit")))

	// Unrelated alerts get nothing.
	assert.False(t, cpu.CanAutoExecute(overcommitCtx("KubePodCrashLooping")))
	assert.False(t, cpu.CanAutoExecute(overcommitCtx("")))
}

// TestOvercommitVariantKeysAreDistinct — auto-discovery dedups by
// implementation type, so four registrations of one type with the same variant
// key would silently collapse to whichever ran first.
func TestOvercommitVariantKeysAreDistinct(t *testing.T) {
	keys := map[string]bool{}
	for _, a := range []*overcommitAction{
		{resource: "cpu"},
		{resource: "memory"},
		{resource: "cpu", byNamespace: true},
		{resource: "memory", byNamespace: true},
	} {
		key := a.VariantKey()
		assert.False(t, keys[key], "variant key %q used twice", key)
		keys[key] = true
	}
	assert.Len(t, keys, 4)
}

// TestOvercommitRegisteredUnderExistingNames — the four names already exist as
// agent_playbook rows and catalog entries. Registering under exactly those is
// what turns those dead rows live, so a rename here silently un-implements them
// again.
func TestOvercommitRegisteredUnderExistingNames(t *testing.T) {
	for _, name := range []string{
		"cpu_overcommited_enricher",
		"memory_overcommited_enricher",
		"cluster_cpu_requests_enricher",
		"cluster_memory_requests_enricher",
	} {
		action, found := playbooks.GetAction(name)
		require.True(t, found, "action %q is not registered", name)

		oc, ok := action.(*overcommitAction)
		require.True(t, ok, "action %q is not the overcommit action", name)
		assert.Equal(t, name, oc.actionName(), "actionName() must match the registered name")
	}
}

func TestOvercommitSummaryTable(t *testing.T) {
	a := &overcommitAction{resource: "cpu"}

	// 40 cores requested against 32 allocatable over 4 nodes, largest 8.
	resp, err := a.summaryTable(32, 40, 8, 4, 210)
	require.NoError(t, err)

	table, ok := resp.(playbooks.PlaybookActionResponseTable)
	require.True(t, ok)

	flat := map[string]string{}
	for _, row := range table.Rows {
		require.Len(t, row, 2)
		flat[row[0].(string)] = row[1].(string)
	}

	assert.Equal(t, "40.00 cores", flat["Requested by pods"])
	assert.Equal(t, "32.00 cores", flat["Allocatable across 4 nodes"])
	assert.Equal(t, "8.00 cores", flat["Over-committed by"])
	assert.Equal(t, "125%", flat["Requests vs allocatable"])
	// The mixin's HA branch compares against allocatable minus the largest
	// node, which is why a cluster that looks fine can still alert.
	assert.Equal(t, "24.00 cores", flat["Allocatable if the largest node is lost"])
	assert.Equal(t, "167%", flat["Requests vs that"])
	assert.Equal(t, "210", flat["Containers with a cpu request set"])

	require.Len(t, table.Insight, 1)
	assert.Contains(t, table.Insight[0].Message, "8.00 cores more than the cluster has")
}

// TestOvercommitSummaryTableWithHeadroom — the alert can be open while the
// numbers have already recovered. The card must say "headroom", not report a
// negative over-commitment.
func TestOvercommitSummaryTableWithHeadroom(t *testing.T) {
	a := &overcommitAction{resource: "memory"}

	resp, err := a.summaryTable(64*1024*1024*1024, 32*1024*1024*1024, 0, 2, 0)
	require.NoError(t, err)
	table := resp.(playbooks.PlaybookActionResponseTable)

	flat := map[string]string{}
	for _, row := range table.Rows {
		flat[row[0].(string)] = row[1].(string)
	}
	assert.Equal(t, "32.0 GiB", flat["Headroom"])
	assert.NotContains(t, flat, "Over-committed by")
	assert.Empty(t, table.Insight, "no warning when the cluster is within capacity")
}

func TestOvercommitNamespaceTable(t *testing.T) {
	a := &overcommitAction{resource: "cpu", byNamespace: true}

	samples := []latestSample{
		{Metric: map[string]string{"namespace": "shop"}, Value: 12},
		{Metric: map[string]string{"namespace": "monitoring"}, Value: 20},
		{Metric: map[string]string{"namespace": "kube-system"}, Value: 4},
		{Metric: map[string]string{"namespace": ""}, Value: 99},    // no namespace — dropped
		{Metric: map[string]string{"namespace": "idle"}, Value: 0}, // nothing reserved — dropped
	}
	resp, err := a.namespaceTable(samples, 32)
	require.NoError(t, err)
	table := resp.(playbooks.PlaybookActionResponseTable)

	require.Len(t, table.Rows, 3)
	// Sorted by what each namespace holds, largest first — the actionable end.
	assert.Equal(t, "monitoring", table.Rows[0][0])
	assert.Equal(t, "20.00 cores", table.Rows[0][1])
	// 20/32 is 62.5%, and Go's %.0f rounds half to even.
	assert.Equal(t, "62%", table.Rows[0][2])
	assert.Equal(t, "shop", table.Rows[1][0])
	assert.Equal(t, "kube-system", table.Rows[2][0])
}

// TestOvercommitNamespaceTableTruncates — a 200-namespace table is a wall, and
// the title has to say it is a top-N or the reader assumes it is everything.
func TestOvercommitNamespaceTableTruncates(t *testing.T) {
	a := &overcommitAction{resource: "cpu", byNamespace: true}

	samples := make([]latestSample, 0, 40)
	for i := 0; i < 40; i++ {
		samples = append(samples, latestSample{
			Metric: map[string]string{"namespace": string(rune('a'+i%26)) + "-ns"},
			Value:  float64(i + 1),
		})
	}
	resp, err := a.namespaceTable(samples, 100)
	require.NoError(t, err)
	table := resp.(playbooks.PlaybookActionResponseTable)

	assert.Len(t, table.Rows, overcommitNamespaceRows)
	assert.Contains(t, table.AdditionalInfo["title"], "top 15")
}

// TestOvercommitNamespaceTableEmpty — an empty table reads as "nothing is
// reserved", the opposite of what fired, so this errors instead.
func TestOvercommitNamespaceTableEmpty(t *testing.T) {
	a := &overcommitAction{resource: "cpu", byNamespace: true}
	_, err := a.namespaceTable(nil, 32)
	assert.Error(t, err)
}

func TestOvercommitFormatting(t *testing.T) {
	cpu := &overcommitAction{resource: "cpu"}
	mem := &overcommitAction{resource: "memory"}

	assert.Equal(t, "2.50 cores", cpu.format(2.5))
	assert.Equal(t, "1.0 GiB", mem.format(1024*1024*1024))
	assert.Equal(t, "512.0 MiB", mem.format(512*1024*1024))
	assert.Equal(t, "0.0 B", mem.format(0))

	assert.Equal(t, "n/a", formatRatio(10, 0), "a zero denominator must not render as Inf%")
	assert.Equal(t, "50%", formatRatio(1, 2))
}

func TestSingleValue(t *testing.T) {
	assert.Equal(t, 0.0, singleValue(nil))
	assert.Equal(t, 5.0, singleValue([]latestSample{{Value: 5}}))
	// A query that changed shape returns several series; the largest is the
	// safest read of a cluster total.
	assert.Equal(t, 7.0, singleValue([]latestSample{{Value: 3}, {Value: 7}, {Value: 1}}))
}

// TestOvercommitQueriesScopedToResource — every query must name this action's
// own resource and no other. A query hardcoded to resource="cpu" on the memory
// action reports a number about a different thing while looking authoritative:
// a container can set a CPU request and no memory request, so the counts really
// do differ.
func TestOvercommitQueriesScopedToResource(t *testing.T) {
	for _, a := range []*overcommitAction{
		{resource: "cpu"},
		{resource: "memory"},
		{resource: "cpu", byNamespace: true},
		{resource: "memory", byNamespace: true},
	} {
		other := "memory"
		if a.resource == "memory" {
			other = "cpu"
		}
		for key, query := range a.queries() {
			assert.Contains(t, query, `resource="`+a.resource+`"`,
				"%s query %q must be scoped to %s", a.resource, key, a.resource)
			assert.NotContains(t, query, `resource="`+other+`"`,
				"%s query %q leaks the %s resource", a.resource, key, other)
		}
	}
}

// TestOvercommitSummaryNamesTheResource — the row label has to say which
// resource the count is about, or a memory card showing a count reads as if it
// were memory when nothing says so.
func TestOvercommitSummaryNamesTheResource(t *testing.T) {
	mem := &overcommitAction{resource: "memory"}
	resp, err := mem.summaryTable(1024, 2048, 0, 2, 17)
	require.NoError(t, err)
	table := resp.(playbooks.PlaybookActionResponseTable)

	found := false
	for _, row := range table.Rows {
		if label, _ := row[0].(string); label == "Containers with a memory request set" {
			found = true
			assert.Equal(t, "17", row[1])
		}
	}
	assert.True(t, found, "memory summary must label the count as memory")
}
