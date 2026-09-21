# Sizing the Nudgebee control plane

How big an install needs to be, what Small / Medium / Large mean, and what to do
when a service uses more memory than this document says it should.

Every number here was measured, not modelled. Five separate deployments, 7-day
windows, working-set memory and CPU per container. Where a figure is an estimate
rather than an observation, it says so.

---

## Pick a tier

**Tiers are sized by the estate you monitor, not by the cluster Nudgebee runs in.**
A 200-node cluster that watches nothing costs us nothing; a 3-node cluster watching
80 remote clusters costs us a lot. What drives our load is the number of clusters,
nodes, pods and cloud accounts we collect from.

| Tier | Monitored clusters | Nodes | Pods | Cloud accounts | Install with |
|---|---|---|---|---|---|
| **Small** | ≤ 5 | ≤ 250 | ≤ 10,000 | ≤ 25 | chart defaults (or `-f values-small.yaml`) |
| **Medium** | ≤ 25 | ≤ 900 | ≤ 100,000 | ≤ 550 | `-f values-medium.yaml` |
| **Large** | ≤ 80 | ≤ 2,000 | ≤ 300,000 | ≤ 500 | `-f values-large.yaml` |

Use the **highest** tier any one column puts you in. An estate with 3 clusters but
400 cloud accounts is Medium, not Small — cloud collection is the single most
expensive thing we do per unit.

```bash
helm upgrade --install nudgebee ./deploy/kubernetes/nudgebee -f values-medium.yaml
```

A bare `helm install` with no profile flag gives you Small. That is deliberate: the
default has to be safe for a real customer, not just for a laptop demo.

### What each tier actually costs

Total across all control-plane services, excluding the database:

| Tier | Memory (avg) | Memory (peak) | CPU (avg) | CPU (peak) | Plan for |
|---|---|---|---|---|---|
| Small | 3.3 – 6.6 GiB | 7.7 – 11.9 GiB | 0.2 – 0.3 cores | ~2.2 cores | 3 CPU / 16 GiB |
| Medium | 11.3 GiB | 27.6 GiB | 0.80 cores | 10.1 cores | 5 CPU / 32 GiB |
| Large | 21.1 GiB | 32.3 GiB | 1.11 cores | 6.6 cores | 8 CPU / 48 GiB |

### Confidence

- **Small and Large are anchored on real deployments.** Large is measured on our
  largest install: 77 connected clusters, 1,763 nodes, 282,432 pods, 433 cloud
  accounts, 149 tenants. Medium is measured on a 24-cluster / 851-node / 93,921-pod
  / 530-account install.
- **Beyond Large is extrapolation.** Do not quote it without a load test.
- **Memory does not scale linearly with the estate.** 3x the pods (94,000 → 282,000)
  cost 1.9x the memory. Most services are flat; only four track estate size at all
  (services-server, ml-k8s-server, k8s-collector-worker, temporal-history).

---

## The rules these numbers follow

**Memory binds before CPU.** Every service here hits its memory limit long before
CPU pressure would matter. Sizing work should start with memory and treat CPU as an
afterthought — which is the opposite of how our charts were written.

**Memory requests come from the average. Memory limits come from the peak.**
Not from the average with a percentage on top. Several services in this platform
burst an order of magnitude above steady state — services-server sits at 90-240Mi
and hits 6.7Gi four times a day. A limit set from average usage is an OOMKill with
a delay on it.

**Never lower a memory limit. Ratchet it up only.** This is the most important
rule on this page and the one that is easiest to talk yourself out of, because a
fresh measurement always makes existing ceilings look wasteful. They are not:

|  | Too high | Too low |
|---|---|---|
| **Request** | Wasted reserved capacity — you pay in nodes | Pod scheduled onto a node that cannot feed it |
| **Limit** | **Costs nothing at steady state** — limits are a ceiling, not a reservation | **OOMKill** |

The costs are not symmetric, so the decision is not symmetric. Trim requests
freely from measurement; treat a limit as a ratchet that only ever goes up.

The reason this rule is written down: the first draft of this very document cut
seven memory limits — `cloud-collector-server` from 3Gi to 1Gi, `cost-server` from
2Gi to 500Mi — on the strength of 7-day measurements. Every one of those
measurements came from an install we run ourselves, four of the five being dev,
test or near-idle. A customer install was then found with `cloud-collector-server`
manually raised to **5Gi**, five times the proposed default. Field reports of
Nudgebee OOMing on default memory were, correctly, the opposite signal.

**Our own deployments are not a representative sample.** Account count, account
size, cluster churn, API throttling and workload mix all vary far more across
customers than across our estate, and a 7-day window sees neither the monthly
batch nor the bad day. Measurement is authoritative for what a service uses at
rest. It is *not* authoritative for what it can peak to under a workload we have
never seen. Where the two disagree, the ceiling wins.

**CPU requests come from p95, not from the limit.** The requests in the old charts
were set at or near the limit, which reserved 5-30x actual usage: 6.35 cores of
scheduler capacity for a Small install that used 0.27. Requests are what the
scheduler reserves and what you pay for in node count; limits are just a ceiling.

Summed, the CPU requests still come out above the measured aggregate peak —
10 cores requested at Large against a 6.6-core measured peak. That is expected
and intended: per-service p95s do not all occur at the same moment, so adding them
up over-counts. Two services are also deliberately above their own p95 —
services-server, because the API tier should not be the thing that gets starved,
and rag-server, because its CPU varies by more than two orders of magnitude with
knowledge-base content and the p95 of a lightly-loaded install is not a safe floor.

**CPU limits are set generously or not at all.** Throttling was measured on eight
services, up to 95% of scheduling periods on one. For bursty work — cloud discovery,
agent workspaces — there is no CPU limit at all: capping it does not save anything,
it just makes the operation take longer and time out.

**Single replica is a decision, not a default.** At Small, most services run one
replica and a rolling upgrade is a brief outage. Medium and above run two on
anything that serves requests or ingests data. `temporal-history` stays at one until
Large because it owns shards and adding replicas triggers a rebalance.

**Every value sits on a round ladder.** A measurement of 268Mi does not become a
`320Mi` request. Precision that the underlying measurement does not support reads as
arbitrary and makes the whole table hard to scan and compare. Snap to:

```
memory   50Mi  100Mi  250Mi  500Mi  750Mi  1Gi  1.5Gi  2Gi  2.5Gi  3Gi  4Gi  5Gi  6Gi  8Gi  12Gi
cpu      25m   50m    100m   150m   200m   250m  300m  400m  500m  600m  750m  1000m
         1500m 2000m  2500m  3000m  4000m
```

Nearest rung, with two hard floors that override it: a **request never lands below
the measured average**, and a **limit never lands below the measured peak**. Where
rounding down would breach either, go up a rung instead. Three values in this chart
were caught by exactly that rule (`app`, `workflow-server` and `llm-server` requests)
and one — the bundled `postgresql` request — deliberately breaks the floor, because
Postgres counts page cache in its working set and expands to fill what it is given,
so its observed average is not a requirement. That exception is argued in the values
file rather than left as a silent outlier.

**A sampled peak is a floor on the real peak, not the peak.** `container_memory_working_set_bytes`
is scraped every 15-30s. An allocation spike shorter than that interval never appears
in the series, so "measured peak" systematically under-reads exactly the bursts that
do the OOM-killing. When a container has been observed OOM-killed against a limit
*higher* than its sampled peak, the limit that failed is the better evidence — size
above it, not above the sample. Three limits here were set that way: `services-server`
(OOM-killed at 2500Mi against an 1804Mi sampled peak), `relay-server` (7 pods, 26
restarts at 1000Mi against an 865Mi sampled peak) and `temporal-history` (OOM-killed
at 2Gi). Cross-check with `kube_pod_container_status_last_terminated_reason{reason="OOMKilled"}`
before trusting a peak, and cross-check the peak itself against `container_memory_rss`
and `container_memory_cache` — if RSS ≈ working set and cache ≈ 0, the number is real
process memory rather than page cache charged to the cgroup.

---

## Known tier inversion: `ml-k8s-server`

One service does not follow the tier table, and it is worth knowing before you
read a rendered manifest and think something is broken.

`ml-k8s-server` OOM-killed in production, and PR #37330 fixed it by pinning the
service to **request = limit = 4Gi** — Guaranteed QoS, the strongest anti-eviction
posture Kubernetes offers. That override lives in the umbrella
`nudgebee/values.yaml`, and a parent chart's values beat a subchart's, so it wins
over the 1.5Gi request in `ml-k8s-server/values.yaml`.

The profile overlays are read *after* the umbrella, so they still apply. The net
effect:

| Tier | Effective memory request | From |
|---|---|---|
| Small | **4Gi** | umbrella override (#37330) |
| Medium | 1.5Gi | `values-medium.yaml` |
| Large | 2Gi | `values-large.yaml` |

Small therefore reserves more than Medium or Large for this one service. That is
backwards, and it is deliberate for now: #37330 was a response to a real outage,
and unpicking someone's live OOM fix as a side effect of a sizing change is the
wrong trade. Limits are unaffected and remain monotonic (4Gi / 5Gi / 6Gi).

The clean resolution, when someone wants it, is to make `ml-k8s-server`
Guaranteed at every tier — request = limit of 4Gi / 5Gi / 6Gi — and delete the
umbrella override so the numbers live in one place again. Until then, do not
"correct" the subchart request on its own; that changes nothing at Small and only
makes the file lie harder.

## Known memory defects

These are the services where measured memory exceeds what the design implies. Each
one has a verdict: **ours** (a defect in our code we should fix) or **load** (real
work, size for it). Sizing around a leak is a stopgap; the fix is the fix.

| Service | What we measured | Verdict | Status |
|---|---|---|---|
| **k8s-collector-worker** | 439Mi → 1051Mi over 7 days, **zero reclaim**, identical on both replicas (~87Mi/day/pod). Curve does not flatten and does not track estate size. | **OURS — leak** | Root cause not isolated. Limits are a containment budget (~12 days at Small, ~1 month at Large) so the pod restarts on its own rather than OOM-looping. **Alert on the restart.** Needs a `tracemalloc` pass on the RabbitMQ consumer. |
| **services-server** | Steady state 90-240Mi. Four times a day, at a fixed 6-hour interval, it runs ~110 minutes at 1.0Gi (Small) to 6.3-7.9Gi (Large). 11 of 26 Large pods crossed 6Gi in an 8-day window. | **OURS — allocation pattern** | Bounded and predictable, not a leak. It is the finops-score recompute cron (`api-server/services/recommendation/finops_score.go:311`) materialising the whole recommendation set in memory. Limits are set from the cron. The real fix is to stream/page the recompute. |
| **workspace-server** | Most sandboxes 10-40Mi; ~1 in 10 exceeds 4Gi on a busy install (7615Mi, 5079Mi, 4358Mi on dev; 6951Mi on prod), and individual pods have been measured at 10.4Gi. | **LOAD — but no guardrail** | Genuine agent work (repo clones, scans, CLI output). The defect was that the memory limit defaulted to empty — i.e. *no limit* — so one tenant's session could take a node. Now defaults to 4Gi (`llm_server_workspace_resource_limit_memory`). Accepted trade: roughly one session in ten gets OOM-killed. Alert on it; raise the setting per install if the rate hurts. |
| **temporal-history** | 2139Mi → 2533Mi over 7 days, zero reclaim. A single-cluster install sat at 1718Mi average / 2043Mi peak and was still climbing; OOM-killed elsewhere against a 2Gi limit. | **UPSTREAM — config** | Temporal's in-memory history and event caches filling toward their configured ceiling. Not our code, but our chart shipped no requests or limits for any temporal role, leaving all four pods BestEffort. Size the limit from the cache ceiling, not the average. |
| **ml-k8s-server** | 818Mi-1.75Gi average, 1.4-4.4Gi peak. Sawtooth — it does release. | **LOAD** | Scales with workloads analysed. Bursty but bounded. Needs headroom, not a fix. |
| **llm-server** | Steady 96-382Mi; one pod spiked to 2.9Gi. | **LOAD** | Transient, marshalling a large conversation payload. Chart previously shipped no memory limit; now bounded. |
| **k8s-collector** (API pod) | 149-369Mi, plateaus. | **LOAD — benign** | Cache warm-up, then flat. No action. |
| **rag-server** | 419-702Mi on most installs; 2.6Gi and 1.4 sustained cores on one carrying benchmark load. | **LOAD — environment-specific** | Not a defect at customer scale. Reranker concurrency was already bounded (commit `eb4941801c`). |

### Why this section exists

Before this document, the response to a memory problem here was to raise the limit.
There are at least seven separate "bump the limit / stop the OOM" commits in the
history across llm-gateway, ml-k8s-server, rag-server, app, services-server and
k8s-collector. None of them asked whether the memory was legitimate. Two of the
cases in the table above are defects that survived years of limit bumps.

**Raising a limit is a valid mitigation. It is not a diagnosis.** Do both.

---

## When a service exceeds its limit

Work in this order. Do not skip to step 3.

### 1. Get the shape, not the number

A single high figure tells you nothing. What matters is how memory behaves over the
pod's lifetime.

```promql
sum by (pod) (container_memory_working_set_bytes{namespace="nudgebee",container="<name>"})
```

Graph it over 7 days. If the pod restarts often, graph a single pod's lifetime.

### 2. Classify the shape

| Shape | Means | Do this |
|---|---|---|
| **Rises steadily, never falls, does not flatten** | Leak. Confirm by checking every replica shows the same slope — a leak is deterministic, load is not. | Profile it (step 3). Set the limit as a containment budget and alert on the restart. |
| **Sawtooth — rises then drops, repeatedly** | Normal allocation and reclaim. | Not a bug. Set the limit from the peak of the sawtooth. |
| **Flat baseline with spikes at a fixed interval** | A scheduled job. | Find the cron. Read the timestamps off the graph and grep for the interval. Size the limit for the job, and ask whether the job should be streaming instead. |
| **Flat baseline with one irregular spike** | A single large request or payload. | Size the limit for the spike. Look for a missing bound on input size. |
| **Higher on bigger deployments, flat within each** | Genuine scaling. | Not a bug. Update the tier tables above with the new coefficient. |

### 3. Attribute it

- **Go services** (services-server, llm-server, relay-server, workflow-server,
  cloud-collector-server, llm-gateway, ticket-server, cost-server): use the pprof
  MCP tooling against the live pod. `inuse_space` tells you what is held; compare
  two snapshots an hour apart to see what is growing.
- **Python services** (ml-k8s-server, rag-server, k8s-collector): `tracemalloc`
  snapshot diff. There is no always-on profiler on these — you will need to
  instrument.
- **Upstream components** (temporal, postgres, rabbitmq, redis, qdrant): it is
  almost always a cache ceiling. Find the config knob before assuming a defect.

### 4. Record the verdict

Add a row to the table above, whichever way it goes. A service that was investigated
and found healthy is as useful to record as one that was found broken — it stops the
next person re-running the same investigation.

If this is the **third** time the same service has had its limit raised, stop
raising it and fix the allocation instead. That threshold has already been passed
twice in this repo.

---

## Keeping this current

Re-measure when the estate crosses a tier boundary, when a new service ships, or
every quarter — whichever comes first. The numbers in the subchart `values.yaml`
files each carry the measurement they came from in a comment; keep that true.

What is **not** covered here: PostgreSQL sizing, which is 60-80% of the total
resource bill on a real install and is dominated by the evidence payload attached to
each stored event. That has its own model.

## Related

- [`deploy/kubernetes/NB_HELM_CHART_GUIDE.md`](../deploy/kubernetes/NB_HELM_CHART_GUIDE.md) — how the umbrella chart is put together
- [`docs/architecture-decisions.md`](architecture-decisions.md) — the why-behind-X log
