# KG Read-Path Scaling Baseline

Baseline for the `kg_*` read APIs + the two filter/dropdown APIs, produced by
[`kg_scaling_bench_test.go`](kg_scaling_bench_test.go). Re-run the same command after a code change
and compare against this file.

| run | date | code under test |
|---|---|---|
| **Jul** | 2026-07-13 | branch `feat/kg-per-resource-sources` |
| **Sep** | 2026-09-10 | `main` @ `d5cce9588d`, after the `kgBenchSettle` fix below |

> **Do not diff Jul and Sep latencies.** The July run (and the first September run) timed reads
> against a tenant seeded milliseconds earlier, which inflated latency by 3–10x unpredictably — see
> *The measurement bug*. **Only the memory columns are comparable across the two runs.** All Sep
> latencies below are settled measurements and are internally consistent.

Three code changes landed between the runs:

- **#35692** (`dcce3f46f0`, Aug 6) — unified KG traversal onto one global-visited BFS engine,
  replacing a per-path-visited recursive CTE that was `O(simple-paths)` rather than `O(V+E)`.
  Rewires `GetMultipleNodeNeighbors` and `TraverseDirectional`.
- **V847** (Aug 5) — two tenant-leading node-search indexes: a `(tenant_id, name, namespace)` btree
  and a trigram GIN for ILIKE.
- **#34814** — `GetFilterOptions` cache + parallelized compute.

`GetCompleteGraphFromDatabase`, `GetNodeNeighbors`, `SearchNodes` and `GetFilterValues` had **zero**
commits in that window, which is why their memory numbers reproduce byte-for-byte.

## How to reproduce

```bash
cd api-server/services
export APP_DATABASE_URL=postgresql://…            # a disposable / local pg, NOT prod
go test -run='^$' -bench=BenchmarkKGReadPathScaling -benchmem -benchtime=1x -timeout=90m ./knowledge_graph/test/
```

Scales overridable via `KG_BENCH_SCALES=1000,25000`. Seeds a Barabási–Albert synthetic graph
(~3 edges/node, realistic hubs) under an isolated bench-tenant UUID and deletes it afterward.
Full sweep takes ~11 min.

## Environment

Both runs: **Apple M4** (`darwin/arm64`), 10 logical CPUs, shared dev Postgres (PG17) reached over a
`cloud-sql` proxy on `127.0.0.1:5432`, `-benchtime=1x` — single shot per case, no averaging.

### Caveats

1. **There is a ~1.0–1.5 s floor** on every call: proxy round-trip + connection setup + Go
   marshalling. Any API sitting at that floor is **not being measured** — its real DB cost is below
   the noise. See *What this benchmark cannot measure*.
2. **Latency still varies ±40% run to run** even settled, at `-benchtime=1x`. Treat it as
   order-of-magnitude; the **B/op / allocs/op / payload_bytes** columns are the stable signal.
3. For a rigorous before/after diff use `-benchtime=10x -count=6` on an idle DB with `benchstat`,
   ideally against a local Postgres so caveat 1 disappears.

---

## The measurement bug (fixed in this revision)

The July baseline reported `GetMultipleNodeNeighbors` as **super-linear** — 8.4 s @1K → 78.4 s @5K,
"does not finish at 25K" — and named it the #1 scaling liability. Most of that was an artifact of
the harness.

The benchmark seeded the tenant inside `b.Run` and immediately timed a read against it, so the timed
window paid two one-off costs unrelated to the code under test:

- **Stale planner stats.** Nothing `ANALYZE`d after the seed. Measured on the frontier query at 5K:
  estimated **4 rows against 9,604 actual** — a 2,400x underestimate, which picks a nested loop.
- **Unset hint bits.** The first reader of each freshly-inserted tuple consults the commit log and
  writes the hint bit back, so the first scan is materially more expensive and dirties pages.

The effect was large and unstable, not a constant offset. The identical operation at 5K measured
**78.4 s / 51.8 s / 35.0 s / 25.4 s** across four runs, against **7.1–9.4 s** for the same call on a
settled tenant (three repetitions, no seeding in the timed window).

`kgBenchSettle` now runs `ANALYZE` on both tables plus a tenant-wide `count(*)` after seeding. With
it, the curve is monotonic and the inversion is gone:

| `MultiNeighbors` @ | before fix | after fix |
|---|---:|---:|
| 5K | 25.4 – 78.4 s | **9.25 s** |
| 25K | 9.15 s | **13.51 s** |

Note the fix made 25K *slower*. The old numbers were not uniformly optimistic — they were noise in
both directions.

---

## Results (settled)

### `GetMultipleNodeNeighbors` — fixed by #35692, and not super-linear

| nodes | Sep latency | returned | B/op | B per node |
|------:|------------:|---------:|-----:|-----------:|
| 1,000 | 4.66 s | 997 | 19,039,496 | 19.1 KB |
| 5,000 | 9.25 s | 4,623 | 96,982,208 | 21.0 KB |
| 25,000 | 13.51 s | 20,409 | 409,109,992 | 20.0 KB |
| 100,000 | 37.94 s | 68,101 | 1,162,985,400 | 17.1 KB |

→ **`O(V+E)`.** Allocation per returned node is flat at 17–21 KB across a 68x range. July's
"does not finish at 25K" is fixed — it now walks a 100K-node tenant in 38 s. Still the heaviest API
in absolute terms at 100K (1.16 GB), but the growth is linear, not super-linear.

### 🔴 `GetCompleteGraphFromDatabase` — unchanged, still unbounded

| nodes | Sep lat | returned (n / e) | payload | Jul B/op | **Sep B/op** | allocs/op |
|------:|--------:|-----------------:|--------:|---------:|-------------:|----------:|
| 1,000 | 2.11 s | 1,000 / 2,994 | 1.86 MB | 14.5 MB | 14,511,280 | 182,395 |
| 5,000 | 1.96 s | 5,000 / 14,994 | 9.33 MB | 84.8 MB | 84,849,600 | 909,995 |
| 25,000 | 7.10 s | 25,000 / 74,994 | 46.7 MB | 390 MB | 390,585,440 | 4,550,014 |
| 100,000 | 19.33 s | 100,000 / 299,994 | **186.4 MB** | *~1.5 GB (extrap.)* | **1,565,748,056** | 18,200,039 |

→ **Linear, and July's 100K extrapolation is confirmed by measurement** (predicted ~185 MB / ~1.5 GB
/ ~18M; measured 186.4 MB / 1.57 GB / 18.2M). One unfiltered call on a 100K-node tenant transiently
allocates **~1.6 GB** and ships a **~186 MB** payload — while the UI caps rendering at 1,500 nodes.
**With traversal fixed, this is now the top scaling liability.**

`GetCompleteGraphFromDatabaseWithFilters` — node filter pushes down, edges **always fully scanned**:

| nodes | Sep lat | returned (n / e) | Jul B/op | **Sep B/op** |
|------:|--------:|-----------------:|---------:|-------------:|
| 1,000 | 1.12 s | 250 / 169 | 5.7 MB | 5,057,464 |
| 5,000 | 1.79 s | 1,250 / 898 | 25–30 MB | 29,680,344 |
| 25,000 | 3.86 s | 6,250 / 4,547 | 144 MB | 144,017,064 |
| 100,000 | 13.22 s | 25,000 / 18,620 | 579 MB | 579,609,144 |

### 🟡 `TraverseDirectional` — the cap bounds the payload, not the work

July marked this 🟢 healthy but only measured 1K and 5K:

| nodes | Sep lat | returned | total_discovered | B/op |
|------:|--------:|---------:|-----------------:|-----:|
| 1,000 | 4.26 s | 500 | 984 | 6,295,472 |
| 5,000 | 4.37 s | 500 | 3,544 | 8,679,920 |
| 25,000 | 5.79 s | 500 | 14,370 | 26,094,920 |
| 100,000 | 6.05 s | 500 | 45,058 | **71,412,040** |

→ Latency near-flat (4.26 → 6.05 s over 100x the graph), but **memory grows 11.3x while the returned
set stays pinned at 500**. It walks 45K nodes to hand back 500. `MaxNodes=500` caps the response,
not the work.

### 🟡 `GetFilterOptions` — now measured; cache-cold cost is up

The first September run measured a stale cache, not a scan (see *What this benchmark cannot
measure*). With `kgBenchCleanup` fixed, `node_count` tracks the scale and these are real cold scans:

| nodes | Jul | **Sep cold** | Sep B/op | `node_count` |
|------:|----:|-------------:|---------:|-------------:|
| 1,000 | 3.1 s / 0.6 MB | 3.79 s | 2,250,160 | 1,000 ✅ |
| 5,000 | 3.3 s / 2.8 MB | 4.54 s | 10,250,608 | 5,000 ✅ |
| 25,000 | 3.8 s / 12.5 MB | 10.77 s | 45,739,504 | 25,000 ✅ |
| 100,000 | 5.1 s / 49.8 MB | 11.31 s | **181,978,400** | 100,000 ✅ |

→ Still linear in memory, and **~3.7x heavier than July at every scale** — the cold path now also
serializes the whole payload into the cache row. Read alone that looks like a regression; it is the
cost side of #34814's trade: one expensive cold call, then cheap warm hits. **The warm path remains
unmeasured** — measuring cold vs warm per scale is outstanding work.

### 🟢 Bounded — flat regardless of graph size

| API | 1K | 5K | 25K | 100K | bound |
|---|---|---|---|---|---|
| `SearchNodes_exact` | 1.50 s | 1.48 s | 1.51 s | 1.50 s | `LIMIT <=100` |
| `SearchNodes_prefix` (`hot-%`) | 1.49 s / 10 | 1.43 s / 50 | 1.09 s / 100 of 250 | 1.00 s / 100 of 1,000 | `LIMIT <=100` |
| `SearchNodes_contains` (`%hot%`) | 1.54 s | 1.59 s | 1.01 s | 0.97 s | `LIMIT <=100` |
| `GetNodeNeighbors` (1-hop) | 3.52 s / 87 n | 2.56 s / 192 n | 3.06 s / 441 n | 4.91 s / 921 n | node degree |
| `GetFilterValues` | 0.55 s | 0.51 s | 0.59 s | 0.59 s | flat (1 DISTINCT scan) |

`GetFilterValues` is the best-behaved API in the suite — 0.55 → 0.59 s from 1K→100K, 51 KB → 54 KB.

**Every `SearchNodes` row sits at the ~1.0–1.5 s floor. Read the next section before treating any of
it as "no change".**

---

## What this benchmark cannot measure

**`SearchNodes` — V847's indexes.** V847 predicted the fuzzy ILIKE path would go from ~90 ms @100K
to ~9 ms. Both indexes are live and the planner uses them — `EXPLAIN ANALYZE` on the largest real
dev tenant (9,001 active nodes):

```
Bitmap Index Scan on idx_kg_node_tenant_qa_name_trgm
  Index Cond: (tenant_id = … AND (query_attributes->>'name') ~~* '%api%')
Execution Time: 5.410 ms
```

The benchmark reports **~1,500 ms** for that call. The query is **0.4%** of it; the rest is proxy
round-trip and marshalling. A 90 ms → 9 ms improvement is ~150x below this harness's noise floor.
**`SearchNodes`'s flat row is not evidence of no change — it is evidence that this benchmark does
not measure `SearchNodes`.** Use `TestKGSeedForExplain` + `EXPLAIN ANALYZE` for index-sensitive APIs.

**`GetFilterOptions` warm path.** Unfiltered calls return early from
`knowledge_graph_filter_options`. Before this revision `kgBenchCleanup` did not delete that table, so
every scale after the first replayed the previous scale's payload (`node_count` pinned at 1,000,
memory *falling* as the graph grew). That is fixed, so the table above is a genuine cold scan — but
warm-hit cost is still unmeasured at every scale.

---

## Summary verdict

- **Fixed:** `GetMultipleNodeNeighbors` — July's #1 liability ("does not finish at 25K") now
  completes at 100K in 38 s, `O(V+E)`, and was never as super-linear as July reported (#35692).
- **Top liability now:** `GetCompleteGraphFromDatabase[WithFilters]` — unchanged and linear;
  ~1.6 GB transient heap + ~186 MB payload per call at 100K. Consider server-side pagination or
  dropping heavy `properties`.
- **Watch:** `TraverseDirectional` — near-flat latency hiding 11.3x memory growth behind a fixed
  500-node cap. `GetFilterOptions` cold path — linear and ~3.7x heavier than July.
- **Healthy:** `GetFilterValues`, `GetNodeNeighbors`.
- **Unmeasured:** `SearchNodes` (index-bound, ~150x below the harness floor — use `EXPLAIN`);
  `GetFilterOptions` warm path.
