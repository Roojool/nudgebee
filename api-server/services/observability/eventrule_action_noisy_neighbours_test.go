package observability

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/services/eventrule/playbooks"
	"nudgebee/services/security"
)

// Moved here with the action itself, from the playbooks stage-2.2 CanAutoExecute table.
// The node name is required: without it the enricher cannot pick the pod's peers, and
// answering for the wrong node is worse than not answering.
func TestNoisyNeighboursCanAutoExecute(t *testing.T) {
	ctxFor := func(aggKey, subjectType, name, namespace, node string) playbooks.PlaybookActionContext {
		return playbooks.NewPlaybookActionContext("t", "a", slog.Default(), playbooks.PlaybookEvent{
			AggregationKey:   aggKey,
			SubjectType:      subjectType,
			SubjectName:      name,
			SubjectNamespace: namespace,
			SubjectNode:      node,
		})
	}
	a := &noisyNeighboursAction{}

	assert.True(t, a.CanAutoExecute(ctxFor("pod_oom_killer_enricher", "pod", "p1", "ns", "node-1")))
	assert.True(t, a.CanAutoExecute(ctxFor("report_crash_loop", "pod", "p1", "ns", "node-1")))
	assert.False(t, a.CanAutoExecute(ctxFor("job_failure", "job", "j1", "ns", "node-1")))
	assert.False(t, a.CanAutoExecute(ctxFor("pod_oom_killer_enricher", "pod", "p1", "ns", "")))
}

// Requests and limits live in the state_container metricset, which carries NO node
// field — asking for them alongside working set returns null for every container
// (verified against a live cluster). So they are a second search keyed by the pods the
// first one found. If that lookup fails the rows still render, with an empty request
// column, rather than the whole card erroring.
func TestNoisyNeighboursSpecsAreKeyedByPodAndContainer(t *testing.T) {
	raw := []byte(`{"aggregations":{"pods":{"buckets":[
	  {"key":"kube-dns-1","containers":{"buckets":[
	     {"key":"kubedns","requested":{"value":73400320},"limit":{"value":220200960}},
	     {"key":"dnsmasq","requested":{"value":20971520},"limit":{"value":null}}]}}]}}}`)
	var resp esNNSpecResponse
	require.NoError(t, json.Unmarshal(raw, &resp))

	specs := map[string]esContainerSpec{}
	for _, pod := range resp.Aggregations.Pods.Buckets {
		for _, c := range pod.Containers.Buckets {
			specs[pod.Key+"/"+c.Key] = esContainerSpec{
				Requested: esNNValue(c.Requested.Value),
				Limit:     esNNValue(c.Limit.Value),
			}
		}
	}

	assert.Equal(t, float64(73400320), specs["kube-dns-1/kubedns"].Requested)
	assert.Equal(t, float64(220200960), specs["kube-dns-1/kubedns"].Limit)
	// A container with no limit reports 0, not a nil deref.
	assert.Equal(t, float64(20971520), specs["kube-dns-1/dnsmasq"].Requested)
	assert.Zero(t, specs["kube-dns-1/dnsmasq"].Limit)
	// A container absent from the spec search reads as zero rather than missing.
	assert.Zero(t, specs["kube-dns-1/absent"].Requested)
}

// esNNFixture decodes a per-container search response the way esNoisyNeighbours does.
// cpu_used is in nanocores, as Elastic Agent reports it.
func esNNFixture(t *testing.T, raw string) esNNResponse {
	t.Helper()
	var resp esNNResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	return resp
}

// The ES path used to report memory only, so an ES-default account lost the CPU half of
// the card that the PromQL path shows. The two lists are ranked independently — the
// container holding the most memory is rarely the one burning the most CPU.
func TestESNoisyNeighboursReportsCPUAlongsideMemory(t *testing.T) {
	resp := esNNFixture(t, `{"aggregations":{"pods":{"buckets":[
	  {"key":"api-1","ns":{"buckets":[{"key":"shop"}]},"containers":{"buckets":[
	     {"key":"api","used":{"value":900},"cpu_used":{"value":250000000}}]}},
	  {"key":"batch-1","ns":{"buckets":[{"key":"jobs"}]},"containers":{"buckets":[
	     {"key":"worker","used":{"value":100},"cpu_used":{"value":1500000000}}]}}]}}}`)
	specs := map[string]esContainerSpec{
		"api-1/api":      {Requested: 800, Limit: 1000, CPURequested: 0.5, CPULimit: 1},
		"batch-1/worker": {Requested: 50, CPURequested: 2},
	}

	d := buildESNoisyNeighbourData("node-a", resp, specs, 15)

	require.True(t, d.CPUMeasured)
	// nanocores → cores
	assert.InDelta(t, 1.75, d.NodeCPUUsed, 1e-9)
	assert.InDelta(t, 1000.0, d.NodeUsed, 1e-9)
	assert.InDelta(t, 850.0, d.TotalRequested, 1e-9)

	require.Len(t, d.Neighbours, 2)
	assert.Equal(t, "api", d.Neighbours[0]["name"], "memory list is ranked by memory")

	require.Len(t, d.CPUNeighbours, 2)
	top := d.CPUNeighbours[0]
	assert.Equal(t, "worker", top["name"], "CPU list is ranked by CPU, independently")
	assert.Equal(t, "batch-1", top["pod_name"])
	assert.Equal(t, "jobs", top["namespace"])
	assert.Equal(t, "node-a", top["node_name"])
	assert.InDelta(t, 1.5, top["cpu_used"], 1e-9)
	assert.InDelta(t, 2.0, top["cpu_requested"], 1e-9)
	assert.InDelta(t, 0.0, top["cpu_limit"], 1e-9)
	assert.InDelta(t, 1.0, d.CPUNeighbours[1]["cpu_limit"], 1e-9)
}

// The node gauge must reflect the whole node, not just the rows shown, so totals are
// summed before each list is cut to topN.
func TestESNoisyNeighboursTotalsTheNodeBeforeCuttingToTopN(t *testing.T) {
	resp := esNNFixture(t, `{"aggregations":{"pods":{"buckets":[
	  {"key":"p1","containers":{"buckets":[{"key":"a","used":{"value":10},"cpu_used":{"value":1000000000}}]}},
	  {"key":"p2","containers":{"buckets":[{"key":"b","used":{"value":30},"cpu_used":{"value":3000000000}}]}},
	  {"key":"p3","containers":{"buckets":[{"key":"c","used":{"value":20},"cpu_used":{"value":2000000000}}]}}]}}}`)

	d := buildESNoisyNeighbourData("node-a", resp, nil, 2)

	assert.InDelta(t, 60.0, d.NodeUsed, 1e-9)
	assert.InDelta(t, 6.0, d.NodeCPUUsed, 1e-9)
	require.Len(t, d.Neighbours, 2)
	require.Len(t, d.CPUNeighbours, 2)
	assert.Equal(t, "b", d.Neighbours[0]["name"])
	assert.Equal(t, "c", d.Neighbours[1]["name"])
	assert.Equal(t, "b", d.CPUNeighbours[0]["name"])
}

// An index without the CPU field must still say "not measured". Reporting 0 CPU would
// claim the node is idle, which is a different and much more misleading statement.
func TestESNoisyNeighboursLeavesCPUUnmeasuredWhenTheIndexHasNone(t *testing.T) {
	resp := esNNFixture(t, `{"aggregations":{"pods":{"buckets":[
	  {"key":"p1","containers":{"buckets":[{"key":"a","used":{"value":10},"cpu_used":{"value":null}}]}}]}}}`)

	d := buildESNoisyNeighbourData("node-a", resp, nil, 15)

	assert.False(t, d.CPUMeasured)
	assert.Empty(t, d.CPUNeighbours)
	require.Len(t, d.Neighbours, 1)

	rendered, err := noisyNeighboursResponse("p1", "ns", nil, d)
	require.NoError(t, err)
	raw, err := json.Marshal(rendered)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "cpu_neighbours")
	assert.NotContains(t, string(raw), "cpu_used")
}

// Both providers render through noisyNeighboursResponse, so the payload the UI consumes
// cannot drift between them. The card reads these fields verbatim.
func TestNoisyNeighboursPayloadShapeIsProviderIndependent(t *testing.T) {
	resp, err := noisyNeighboursResponse("p1", "ns", map[string]any{"node_name": "n1"},
		&noisyNeighbourData{
			NodeName: "n1", NodeUsed: 100, NodeAllocatable: 200, TotalRequested: 150,
			Neighbours: []map[string]any{{"name": "c1", "pod_name": "p1", "memory_used": 100.0}},
		})
	require.NoError(t, err)

	out, err := json.Marshal(resp)
	require.NoError(t, err)
	for _, want := range []string{"node_name", "memory_used", "memory_allocatable", "memory_requested", "total_pods", "neighbours"} {
		assert.Containsf(t, string(out), want, "payload missing %q", want)
	}
}

// The dispatch to the Elasticsearch path compared against "elasticsearch", a name the
// registry never produces, so no account ever reached it. The provider/source pair
// here is exactly what GetLogsMetricsTracesProvider returns for an ES integration.
func TestNoisyNeighboursRoutesElasticsearchAccountsToTheESPath(t *testing.T) {
	assert.True(t, usesElasticsearchMetrics("ES", "user"),
		"an ES metrics integration must take the Elasticsearch path")

	assert.False(t, usesElasticsearchMetrics("elasticsearch", "user"),
		"not a registered provider name — the string the old comparison looked for")

	// Deliberately false, and the reason this asks the registry instead of comparing
	// provider == "ES": there is no agent-sourced Elasticsearch METRICS source (the
	// ("ES","agent") registration is for logs), and the ES implementation reads its
	// connection from GetElasticsearchConfig, which only accepts user-sourced
	// integrations. Routing ("ES","agent") here would fail on config lookup.
	assert.False(t, usesElasticsearchMetrics("ES", "agent"),
		"no agent-sourced Elasticsearch metrics source exists")
	for _, p := range []struct{ provider, source string }{
		{"prometheus", "user"},
		{"prometheus", "agent"},
		{"datadog", "user"},
		{"", ""},
	} {
		assert.Falsef(t, usesElasticsearchMetrics(p.provider, p.source),
			"%s/%s must stay on the PromQL path", p.provider, p.source)
	}
}

// Drives Execute itself, not just the helper: the dispatch line is where the original
// bug was, and a test of the helper alone stays green if that line is reverted.
// One row per branch of fetchNoisyNeighbours.
func TestNoisyNeighboursExecuteDispatchesOnTheDefaultMetricsProvider(t *testing.T) {
	origProvider, origES, origProm := noisyNeighboursMetricsProvider, fetchESNoisyNeighbours, fetchPromNoisyNeighbours
	t.Cleanup(func() {
		noisyNeighboursMetricsProvider, fetchESNoisyNeighbours, fetchPromNoisyNeighbours = origProvider, origES, origProm
	})

	type call struct{ impl, provider, source string }
	var calls []call
	data := func(node string) *noisyNeighbourData {
		return &noisyNeighbourData{NodeName: node, Neighbours: []map[string]any{}}
	}
	fetchESNoisyNeighbours = func(_ *security.RequestContext, _, node string, _, _ int) (*noisyNeighbourData, error) {
		calls = append(calls, call{impl: "es"})
		return data(node), nil
	}
	promFails := false
	fetchPromNoisyNeighbours = func(_ *security.RequestContext, _, provider, source, node string, _, _ time.Time) (*noisyNeighbourData, error) {
		calls = append(calls, call{impl: "prom", provider: provider, source: source})
		if promFails {
			return nil, errors.New("agent not connected")
		}
		return data(node), nil
	}

	// node_name is passed so Execute does not need the pod inventory to find it.
	params := map[string]any{"pod_name": "api-1", "namespace": "shop", "node_name": "node-a"}
	ctx := impactedServicesCtx("tenant-1", "acc", "pod_oom_killer_enricher", "pod", "api-1", "shop")

	cases := []struct {
		name             string
		provider, source string
		lookupErr        error
		promFails        bool
		want             []call
		wantCard         bool
		wantErr          bool
	}{
		{name: "an ES default is answered from Elasticsearch",
			provider: "ES", source: "user", want: []call{{impl: "es"}}, wantCard: true},
		{name: "a Prometheus default is asked through the resolved source",
			provider: "prometheus", source: "user", want: []call{{"prom", "prometheus", "user"}}, wantCard: true},
		{name: "no configured provider falls to the agent's Prometheus",
			provider: "prometheus", source: "agent", want: []call{{"prom", "prometheus", "agent"}}, wantCard: true},
		{name: "a Chronosphere default is queried as Chronosphere",
			provider: "chronosphere", source: "user", want: []call{{"prom", "chronosphere", "user"}}, wantCard: true},
		{name: "a provider with no implementation falls back to the account's Prometheus",
			provider: "datadog", source: "user", want: []call{{"prom", "prometheus", ""}}, wantCard: true},
		{name: "with no Prometheus to fall back to, the card is skipped rather than failed",
			provider: "datadog", source: "user", promFails: true, want: []call{{"prom", "prometheus", ""}}},
		{name: "a failed provider lookup is an error, not a guess",
			lookupErr: errors.New("db down"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, promFails = nil, tc.promFails
			noisyNeighboursMetricsProvider = func(*security.RequestContext, string, string, string, string) (string, string, error) {
				return tc.provider, tc.source, tc.lookupErr
			}
			resp, err := (&noisyNeighboursAction{}).Execute(ctx, params)
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, calls)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, calls)
			if tc.wantCard {
				assert.NotNil(t, resp)
			} else {
				assert.Nil(t, resp)
			}
		})
	}
}

// The real PromQL implementation, through the metrics layer: the resolved provider is
// what reaches FetchMetricsQuery, not a hardcoded "prometheus".
func TestPromNoisyNeighboursAsksTheResolvedProvider(t *testing.T) {
	recorder := stubUserPrometheus(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	})
	now := time.Now().UTC()

	_, err := promNoisyNeighbours(testRequestContext(), "acc", "prometheus", "user", "node-a", now.Add(-10*time.Minute), now)

	// Every query comes back empty, so the node looks unknown to this Prometheus —
	// an error, not an idle-node card.
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no metrics for node "node-a"`)
	require.NotEmpty(t, recorder.all())
	assert.Contains(t, recorder.all()[0].URL.Query().Get("query"), `node="node-a"`)
}

// Without a node there is nothing to narrow by, and an unfiltered query would return the
// whole cluster's containers as though they were this node's. Guarded inside the lookup
// so every caller is covered, not just the enricher.
func TestNoisyNeighboursESRefusesAnEmptyNodeName(t *testing.T) {
	_, err := esNoisyNeighbours(nil, "acct", "", 10, 15)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node name required")
}

// A node with nothing on it must render as an empty list. A nil slice marshals to
// `null`, which the card iterates.
func TestNoisyNeighboursEmptyNodeRendersAsEmptyList(t *testing.T) {
	resp, err := noisyNeighboursResponse("p1", "ns", nil, &noisyNeighbourData{
		NodeName: "n1", Neighbours: []map[string]any{},
	})
	require.NoError(t, err)
	raw, err := json.Marshal(resp)
	require.NoError(t, err)

	// The response nests the payload as a JSON *string*, so decode twice rather than
	// substring-matching the escaped form.
	var envelope struct {
		Data string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	var payload struct {
		Data struct {
			Neighbours []map[string]any `json:"neighbours"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope.Data), &payload))
	assert.NotNil(t, payload.Data.Neighbours, "must serialise as [] so the card can iterate it")
	assert.Empty(t, payload.Data.Neighbours)
	assert.Contains(t, envelope.Data, `"neighbours":[]`)
}

// The alert-sourced events are the ones that actually suggest contention, and
// they were all excluded — so the question "is a neighbour to blame?" was never
// asked for a throttled container or a pod that never became ready.
func TestNoisyNeighboursRunsForContentionAlerts(t *testing.T) {
	ctxFor := func(aggKey string) playbooks.PlaybookActionContext {
		return playbooks.NewPlaybookActionContext("t", "a", slog.Default(), playbooks.PlaybookEvent{
			AggregationKey:   aggKey,
			SubjectType:      "pod",
			SubjectName:      "p1",
			SubjectNamespace: "ns",
			SubjectNode:      "node-1",
		})
	}
	a := &noisyNeighboursAction{}

	for _, key := range []string{
		"CPUThrottlingHigh",
		"KubePodNotReady",
		"KubeContainerWaiting",
		"KubePodCrashLooping",
		"Kubernetes Warning Event",
	} {
		assert.True(t, a.CanAutoExecute(ctxFor(key)), "expected %s to enrich with neighbours", key)
	}

	// Still scoped: unrelated events do not drag the node's whole neighbourhood in.
	assert.False(t, a.CanAutoExecute(ctxFor("job_failure")))
	assert.False(t, a.CanAutoExecute(ctxFor("image_pull_backoff_reporter")))
}

// A node pinned on CPU starves everything on it while memory looks fine, so the
// payload has to carry CPU for a reader to reach the right answer.
func TestNoisyNeighboursPayloadCarriesCPUWhenMeasured(t *testing.T) {
	resp, err := noisyNeighboursResponse("p1", "ns", nil, &noisyNeighbourData{
		NodeName:        "node-1",
		CPUMeasured:     true,
		NodeCPUUsed:     5.994,
		NodeCPUCapacity: 6,
		CPUNeighbours: []map[string]any{
			{"pod_name": "batch-job", "namespace": "other-team", "cpu_used": 5.2},
		},
	})
	require.NoError(t, err)

	data := payloadData(t, resp)
	assert.Equal(t, 5.994, data["cpu_used"])
	assert.Equal(t, float64(6), data["cpu_allocatable"])
	neighbours, ok := data["cpu_neighbours"].([]any)
	require.True(t, ok, "cpu_neighbours missing from payload")
	require.Len(t, neighbours, 1)
	first, ok := neighbours[0].(map[string]any)
	require.True(t, ok)
	// The whole point: the culprit's namespace is named, not just the pod.
	assert.Equal(t, "other-team", first["namespace"])
}

// Elasticsearch clusters cannot report CPU here. Reporting zero would read as
// "nothing is using CPU", which is a stronger and wronger claim than silence.
func TestNoisyNeighboursOmitsCPUWhenNotMeasured(t *testing.T) {
	resp, err := noisyNeighboursResponse("p1", "ns", nil, &noisyNeighbourData{
		NodeName:   "node-1",
		Neighbours: []map[string]any{},
	})
	require.NoError(t, err)

	data := payloadData(t, resp)
	assert.NotContains(t, data, "cpu_used")
	assert.NotContains(t, data, "cpu_allocatable")
	assert.NotContains(t, data, "cpu_neighbours")
	// The memory half is unchanged for the existing card.
	assert.Contains(t, data, "memory_used")
}

// payloadData unwraps the response envelope, whose `data` is itself a JSON
// string, and returns the card's data object.
func payloadData(t *testing.T, resp playbooks.PlaybookActionResponse) map[string]any {
	t.Helper()
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var envelope struct {
		Data string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope.Data), &payload))
	return payload.Data
}

// Node resolution goes to the pod's inventory row, not to the event's fields.
// KubePodCrashLooping carries the KSM scrape address in subject_node
// ("10.64.21.224:8080") and produced 0 cards from 124 events on test because
// every query filtered on a node by that name and matched nothing.
//
// There is no metastore in a unit test, so the lookup returns "" and execution
// reaches the fallback — which is what makes the ordering observable here.
func TestNoisyNeighboursNodeNameComesFromThePodRow(t *testing.T) {
	podEvent := func(subjectNode string, labels map[string]string) playbooks.PlaybookActionContext {
		return playbooks.NewPlaybookActionContext("t", "a", slog.Default(), playbooks.PlaybookEvent{
			AggregationKey:   "KubePodCrashLooping",
			SubjectType:      "pod",
			SubjectName:      "payments-79b78b4c7b-2rr7d",
			SubjectNamespace: "shop",
			SubjectNode:      subjectNode,
			Labels:           labels,
		})
	}

	// An address in any event field is never used as a node name: not from
	// subject_node, and not from the `instance` label that put it there.
	assert.Empty(t, noisyNeighboursNodeName(podEvent("", map[string]string{"instance": "10.64.21.224:8080"})),
		"the instance label is a scrape address, not a node")
	assert.Empty(t, noisyNeighboursNodeName(podEvent("", map[string]string{"instance": "10.64.21.224"})),
		"a port-less address is still an address — no node is named by IP")

	// obj.spec.nodeName from an agent-sourced event is a real node name, and
	// remains usable when the inventory has never seen the pod.
	assert.Equal(t, "gke-example-cluster-spot-pool-02132c6e-z2nc",
		noisyNeighboursNodeName(podEvent("gke-example-cluster-spot-pool-02132c6e-z2nc", nil)),
		"a real node name on the event is the fallback when the pod row is missing")

	// A node-subject event has no pod to look up and names the node itself.
	nodeEvent := playbooks.NewPlaybookActionContext("t", "a", slog.Default(), playbooks.PlaybookEvent{
		AggregationKey: "node_not_ready",
		SubjectType:    "node",
		SubjectName:    "gke-example-cluster-default-pool-597d4e75-23sr",
	})
	assert.Equal(t, "gke-example-cluster-default-pool-597d4e75-23sr", noisyNeighboursNodeName(nodeEvent))
}
