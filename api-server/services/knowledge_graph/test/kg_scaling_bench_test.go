package test

// KG read-path scaling benchmark (DB-gated).
//
// Seeds a synthetic tenant graph at increasing node/edge counts and measures the
// latency + allocations of every kg_* read method, so we can see which APIs scale
// linearly with graph size (the unbounded full-tenant loaders) and which stay flat
// (the capped/degree-bounded ones) BEFORE a large tenant forces the question in prod.
//
// It reuses the Tier-B harness: gated by testenv.RequireMetastore (SKIPS cleanly when
// APP_DATABASE_URL is unset), seeds via Service.SaveNodes/SaveEdges, and cleans up by
// deleting the ephemeral benchmark tenant. It touches ONLY that tenant UUID.
//
// Run (small smoke):
//   KG_BENCH_SCALES=1000,5000 go test -bench=BenchmarkKGReadPathScaling -benchmem \
//       -run=^$ -benchtime=3x ./knowledge_graph/test/
// Run (full curve): APP_DATABASE_URL=... go test -bench=BenchmarkKGReadPathScaling \
//       -benchmem -run=^$ -benchtime=5x ./knowledge_graph/test/
//
// Custom per-op metrics (nodes_returned / edges_returned / payload_bytes) are the
// scaling curve: plot them against the nodes=N sub-benchmark to see the blow-up.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"nudgebee/services/internal/database"
	"nudgebee/services/internal/testenv"
	"nudgebee/services/knowledge_graph/core"
)

// Dedicated benchmark tenant/account UUIDs — distinct from the Tier-B suite's, so a
// benchmark run never collides with tests or real tenants and cleanup can wipe by tenant_id.
const (
	kgBenchTenant  = "b0000000-0000-4000-8000-000000000001"
	kgBenchAccount = "b0000000-0000-4000-8000-0000000000a1"
	kgBenchSync    = int64(1)
	kgBenchDegree  = 3 // avg out-degree ≈ the live node:edge ratio (~2.5)
)

// defaultBenchScales spans ~current largest tenant (34K) up to ~6× it. Override with
// KG_BENCH_SCALES=1000,25000 for a faster run.
var defaultBenchScales = []int{1_000, 5_000, 25_000, 100_000, 200_000}

// benchRelTypes are all FK-valid relationship_type names; varying them avoids the
// (source,dest,rel,tenant) dedup collapsing parallel edges into one.
var benchRelTypes = []core.RelationshipType{
	core.RelationshipRunsOn,
	core.RelationshipBelongsTo,
	core.RelationshipCalls,
	core.RelationshipManages,
}

// benchNodeTypes is a small mix so node_type filters in the read paths do real work.
var benchNodeTypes = []core.NodeType{
	core.NodeTypeWorkload,
	core.NodeTypePod,
	core.NodeTypeService,
	core.NodeTypeNode,
}

func benchScales(b *testing.B) []int {
	raw := os.Getenv("KG_BENCH_SCALES")
	if raw == "" {
		return defaultBenchScales
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 {
			b.Fatalf("invalid KG_BENCH_SCALES entry %q: %v", part, err)
		}
		out = append(out, n)
	}
	return out
}

// buildSyntheticGraph produces nodeCount nodes and ~nodeCount*avgDegree edges using
// Barabási–Albert preferential attachment: each new node attaches to `avgDegree`
// earlier nodes chosen with probability proportional to their current degree. This
// yields a realistic hub/degree distribution (a few very-high-degree nodes) and
// multi-hop paths. Edges are oriented existing→new so early hub nodes accumulate high
// OUT-degree, giving downstream traversal a genuine worst case.
//
// Returns the nodes, edges, and the ID of the highest-degree node to use as the
// traversal/neighbour seed.
func buildSyntheticGraph(nodeCount, avgDegree int, tenant, account string) ([]*core.DbNode, []*core.DbEdge, string) {
	rng := rand.New(rand.NewSource(1)) // deterministic

	nodes := make([]*core.DbNode, 0, nodeCount)
	degree := make([]int, nodeCount)
	// repeatedNodes is the BA "attachment list": each node index appears once per
	// incident edge, so uniform sampling from it is degree-weighted sampling.
	repeatedNodes := make([]int, 0, nodeCount*avgDegree*2)

	for i := 0; i < nodeCount; i++ {
		nt := benchNodeTypes[i%len(benchNodeTypes)]
		name := fmt.Sprintf("bench-node-%06d", i)
		// ~1% of names share the "hot-" prefix so the SearchNodes benchmark matches a
		// known, non-trivial (but bounded) row set rather than 0 or everything.
		if i%100 == 0 {
			name = "hot-" + name
		}
		// 6-part unique key: {provider}:{account}:{location}:{NodeType}:{hierarchy}:{name}
		uniqueKey := fmt.Sprintf("k8s:%s::%s::%s", account, nt, name)
		props := map[string]interface{}{
			"name":      name,
			"namespace": fmt.Sprintf("ns-%d", i%50),
		}
		nodes = append(nodes, core.NewNode(nt, uniqueKey, props, tenant, account, "benchmark"))
	}

	edges := make([]*core.DbEdge, 0, nodeCount*avgDegree)
	seen := make(map[string]struct{}, nodeCount*avgDegree)
	for i := 1; i < nodeCount; i++ {
		targets := pickTargets(rng, repeatedNodes, i, avgDegree)
		for _, t := range targets {
			// Orient existing(t)→new(i): early nodes gain high out-degree (hubs).
			rel := benchRelTypes[(i+t)%len(benchRelTypes)]
			key := strconv.Itoa(t) + ">" + strconv.Itoa(i) + ":" + string(rel)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			e := core.NewEdge(nodes[t].ID, nodes[i].ID, rel, map[string]interface{}{}, tenant, account, "benchmark")
			e.IsActive = true
			e.LastSyncVersion = kgBenchSync
			edges = append(edges, e)

			degree[t]++
			degree[i]++
			repeatedNodes = append(repeatedNodes, t, i)
		}
	}

	// Highest-degree node = worst-case seed.
	hub := 0
	for i := 1; i < nodeCount; i++ {
		if degree[i] > degree[hub] {
			hub = i
		}
	}
	return nodes, edges, nodes[hub].ID
}

// pickTargets returns up to `k` distinct earlier-node indices (< i), degree-weighted
// via repeatedNodes, falling back to uniform for the first few nodes.
func pickTargets(rng *rand.Rand, repeatedNodes []int, i, k int) []int {
	out := make([]int, 0, k)
	used := make(map[int]struct{}, k)
	for attempt := 0; attempt < k*4 && len(out) < k; attempt++ {
		var t int
		if len(repeatedNodes) == 0 {
			t = rng.Intn(i)
		} else {
			t = repeatedNodes[rng.Intn(len(repeatedNodes))]
		}
		if t >= i {
			t = rng.Intn(i)
		}
		if _, dup := used[t]; dup {
			continue
		}
		used[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func BenchmarkKGReadPathScaling(b *testing.B) {
	dbm := testenv.RequireMetastore(b)
	reqCtx := createTestRequestContext(kgBenchTenant)
	svc := core.NewService(reqCtx, e2eDiscardLogger(), dbm)

	for _, scale := range benchScales(b) {
		b.Run(fmt.Sprintf("nodes=%d", scale), func(b *testing.B) {
			// Seed inside b.Run so a filtered run (e.g. -bench=.../nodes=1000/…)
			// only seeds the scale it actually runs, not every scale. Untimed:
			// b.ResetTimer in each sub-benchmark excludes it. Start clean in case a
			// prior run was interrupted; defer teardown so the tenant is wiped even
			// if a sub-benchmark calls b.Fatalf.
			nodes, edges, hubID := buildSyntheticGraph(scale, kgBenchDegree, kgBenchTenant, kgBenchAccount)
			kgBenchCleanup(b, dbm, kgBenchTenant)
			if err := svc.SaveNodes(nodes, kgBenchSync); err != nil {
				b.Fatalf("SaveNodes(%d): %v", scale, err)
			}
			if err := svc.SaveEdges(edges, nodes, kgBenchSync); err != nil {
				b.Fatalf("SaveEdges(%d): %v", scale, err)
			}
			kgBenchSettle(b, dbm, kgBenchTenant)
			defer kgBenchCleanup(b, dbm, kgBenchTenant)

			b.Run("CompleteGraph", func(b *testing.B) {
				b.ResetTimer()
				var g core.KnowledgeGraph
				for i := 0; i < b.N; i++ {
					var err error
					g, err = svc.GetCompleteGraphFromDatabase(reqCtx, kgBenchTenant)
					if err != nil {
						b.Fatalf("GetCompleteGraphFromDatabase: %v", err)
					}
				}
				reportGraph(b, g)
			})

			b.Run("CompleteGraphFilterByType", func(b *testing.B) {
				filters := &core.GraphFilters{NodeTypes: []core.NodeType{core.NodeTypePod}}
				b.ResetTimer()
				var g core.KnowledgeGraph
				for i := 0; i < b.N; i++ {
					var err error
					g, err = svc.GetCompleteGraphFromDatabaseWithFilters(reqCtx, kgBenchTenant, filters)
					if err != nil {
						b.Fatalf("GetCompleteGraphFromDatabaseWithFilters: %v", err)
					}
				}
				reportGraph(b, g)
			})

			b.Run("NodeNeighbors_hub", func(b *testing.B) {
				b.ResetTimer()
				var g core.KnowledgeGraph
				for i := 0; i < b.N; i++ {
					var err error
					g, err = svc.GetNodeNeighbors(reqCtx, hubID)
					if err != nil {
						b.Fatalf("GetNodeNeighbors: %v", err)
					}
				}
				reportGraph(b, g)
			})

			b.Run("MultiNeighbors_L3_hub", func(b *testing.B) {
				b.ResetTimer()
				var g core.KnowledgeGraph
				for i := 0; i < b.N; i++ {
					var err error
					g, err = svc.GetMultipleNodeNeighbors(reqCtx, []string{hubID}, 3, nil, true)
					if err != nil {
						b.Fatalf("GetMultipleNodeNeighbors: %v", err)
					}
				}
				reportGraph(b, g)
			})

			// SearchNodes name paths — the LLM discovery hot path. Three shapes with
			// distinct index behaviour: exact (btree-indexable), prefix ILIKE and
			// contains ILIKE (NOT servable by a plain btree — the fuzzy scan). Each is
			// paired with the indexed tenant/is_active predicates. runSearch shares the
			// loop so only the params differ.
			runSearch := func(b *testing.B, params core.SearchNodesParams) {
				b.ResetTimer()
				var resp *core.SearchNodesResponse
				for i := 0; i < b.N; i++ {
					var err error
					resp, err = svc.SearchNodes(kgBenchTenant, params)
					if err != nil {
						b.Fatalf("SearchNodes: %v", err)
					}
				}
				if resp != nil {
					b.ReportMetric(float64(len(resp.Nodes)), "nodes_returned")
					b.ReportMetric(float64(resp.TotalCount), "total_matched")
				}
			}
			// Exact name = 1 row; i=42 is not a "hot-" node (42%100 != 0).
			b.Run("SearchNodes_exact", func(b *testing.B) {
				runSearch(b, core.SearchNodesParams{Name: "bench-node-000042", Limit: 100})
			})
			// Prefix ILIKE — ~1% of names ("hot-%"). Anchored, but a plain btree still
			// can't serve ILIKE, so today this scans; a trigram index fixes it.
			b.Run("SearchNodes_prefix", func(b *testing.B) {
				runSearch(b, core.SearchNodesParams{NamePattern: "hot-%", Limit: 100})
			})
			// Contains ILIKE — same ~1% match but leading wildcard ("%hot%"): the worst
			// case, only a trigram GIN can index it.
			b.Run("SearchNodes_contains", func(b *testing.B) {
				runSearch(b, core.SearchNodesParams{NamePattern: "%hot%", Limit: 100})
			})

			// Dropdown / filter-panel APIs. GetFilterOptions fires several
			// SELECT DISTINCT full-tenant scans (account_id, node_type,
			// jsonb_object_keys(labels|query_attributes)) plus a unique_key→id map
			// over every node — none index-backed. GetFilterValues runs one
			// DISTINCT query_attributes->>key scan for a single dropdown's values.
			b.Run("FilterOptions", func(b *testing.B) {
				b.ResetTimer()
				var opts *core.FilterOptions
				for i := 0; i < b.N; i++ {
					var err error
					opts, err = svc.GetFilterOptions(kgBenchTenant, nil, nil)
					if err != nil {
						b.Fatalf("GetFilterOptions: %v", err)
					}
				}
				if opts != nil {
					b.ReportMetric(float64(len(opts.NodeTypes)), "node_types")
					b.ReportMetric(float64(len(opts.AttributeKeys)), "attr_keys")
					b.ReportMetric(float64(opts.NodeCount), "node_count")
				}
			})

			b.Run("FilterValues_namespace", func(b *testing.B) {
				b.ResetTimer()
				var resp *core.FilterValuesResponse
				for i := 0; i < b.N; i++ {
					var err error
					resp, err = svc.GetFilterValues(kgBenchTenant, "attribute", "namespace", nil, nil)
					if err != nil {
						b.Fatalf("GetFilterValues: %v", err)
					}
				}
				if resp != nil {
					b.ReportMetric(float64(len(resp.Values)), "values_returned")
				}
			})

			b.Run("TraverseDownstream_L3_hub", func(b *testing.B) {
				params := core.TraverseParams{
					NodeIDs:   []string{hubID},
					Direction: core.TraverseDirectionDownstream,
					MaxDepth:  3,
					MaxNodes:  500,
				}
				b.ResetTimer()
				var resp *core.TraverseResponse
				for i := 0; i < b.N; i++ {
					var err error
					resp, err = svc.TraverseDirectional(kgBenchTenant, params)
					if err != nil {
						b.Fatalf("TraverseDirectional: %v", err)
					}
				}
				if resp != nil {
					b.ReportMetric(float64(resp.TotalDiscovered), "total_discovered")
					b.ReportMetric(float64(len(resp.Graph.Nodes)), "nodes_returned")
					if resp.Truncated {
						b.ReportMetric(1, "truncated")
					}
				}
			})
		})
	}
}

// reportGraph emits the scaling metrics for a KnowledgeGraph result: node/edge counts
// and the marshalled payload size (exposes the unbounded-payload full-graph loaders).
func reportGraph(b *testing.B, g core.KnowledgeGraph) {
	b.Helper()
	b.ReportMetric(float64(len(g.Nodes)), "nodes_returned")
	b.ReportMetric(float64(len(g.Edges)), "edges_returned")
	if blob, err := json.Marshal(g); err == nil {
		b.ReportMetric(float64(len(blob)), "payload_bytes")
	}
}

// TestKGSeedForExplain seeds the bench tenant at KG_SEED_SCALE nodes and leaves the data
// in place (no cleanup) so an external EXPLAIN ANALYZE can measure DB-side query cost at
// that scale — the network-independent signal the end-to-end benchmark can't give when the
// DB is a remote proxy. Gated: only runs when KG_SEED_SCALE is set. Clean up afterward with
// DELETE FROM knowledge_graph_{edge,node} WHERE tenant_id = '<kgBenchTenant>'.
func TestKGSeedForExplain(t *testing.T) {
	raw := os.Getenv("KG_SEED_SCALE")
	if raw == "" {
		t.Skip("set KG_SEED_SCALE=N to seed the bench tenant for EXPLAIN measurement")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		t.Fatalf("invalid KG_SEED_SCALE %q: %v", raw, err)
	}
	dbm := testenv.RequireMetastore(t)
	reqCtx := createTestRequestContext(kgBenchTenant)
	svc := core.NewService(reqCtx, e2eDiscardLogger(), dbm)

	nodes, edges, _ := buildSyntheticGraph(n, kgBenchDegree, kgBenchTenant, kgBenchAccount)
	for _, table := range []string{"knowledge_graph_edge", "knowledge_graph_node", "knowledge_graph_filter_options"} {
		if _, err := dbm.Exec("DELETE FROM "+table+" WHERE tenant_id = $1::uuid", kgBenchTenant); err != nil {
			t.Fatalf("clean start delete from %s: %v", table, err)
		}
	}
	if err := svc.SaveNodes(nodes, kgBenchSync); err != nil {
		t.Fatalf("SaveNodes(%d): %v", n, err)
	}
	if err := svc.SaveEdges(edges, nodes, kgBenchSync); err != nil {
		t.Fatalf("SaveEdges(%d): %v", n, err)
	}
	t.Logf("seeded %d nodes into bench tenant %s (NOT cleaned up)", n, kgBenchTenant)
}

// kgBenchSettle makes the freshly-seeded tenant readable at steady-state cost, so the
// timed window measures the query rather than the after-effects of the bulk insert.
//
// Without this, a sub-benchmark times the FIRST read of tuples that were inserted
// milliseconds earlier, and pays two one-off costs that have nothing to do with the code
// under test:
//
//   - Planner stats are stale. Nothing ANALYZEs after the seed, so the planner sizes the
//     traversal from pre-seed statistics. Measured on the frontier query at 5K: estimated
//     4 rows against 9,604 actual, a 2,400x underestimate, which picks a nested loop.
//   - Hint bits are unset. The first reader of each new tuple consults the commit log and
//     writes the hint bit back, so the first scan is materially more expensive than the
//     second and dirties pages as a side effect.
//
// The effect is large and unstable, not a constant offset that could be subtracted:
// MultiNeighbors at 5K measured 78.4s / 51.8s / 35.0s / 25.4s across four runs of the
// identical operation, against 7.1-9.4s for the same call on a settled tenant.
func kgBenchSettle(b *testing.B, dbm *database.DatabaseManager, tenantID string) {
	b.Helper()
	for _, table := range []string{"knowledge_graph_node", "knowledge_graph_edge"} {
		// Refresh planner statistics for the newly-inserted rows.
		if _, err := dbm.Exec("ANALYZE " + table); err != nil {
			b.Logf("kg bench settle: ANALYZE %s failed: %v", table, err)
		}
		// Touch every tuple in the bench tenant so hint bits are set before timing.
		if _, err := dbm.Exec(
			"SELECT count(*) FROM "+table+" WHERE tenant_id = $1::uuid", tenantID); err != nil {
			b.Logf("kg bench settle: hint-bit scan of %s failed: %v", table, err)
		}
	}
}

// kgBenchCleanup deletes all KG rows for the benchmark tenant (edges before nodes).
// knowledge_graph_filter_options MUST be included: GetFilterOptions serves an unfiltered
// call from that cache, so a row left behind makes every later scale replay the previous
// scale's payload instead of measuring a real scan.
func kgBenchCleanup(b *testing.B, dbm *database.DatabaseManager, tenantID string) {
	b.Helper()
	for _, table := range []string{"knowledge_graph_edge", "knowledge_graph_node", "knowledge_graph_filter_options"} {
		if _, err := dbm.Exec("DELETE FROM "+table+" WHERE tenant_id = $1::uuid", tenantID); err != nil {
			b.Logf("kg bench cleanup: failed to delete from %s: %v", table, err)
		}
	}
}
