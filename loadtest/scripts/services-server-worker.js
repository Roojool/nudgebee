// services-server worker pod: fires crons at /rpc-cron the way runbook-server's schedules
// do, but all at once and optionally repeated, to see how the background-job pod copes
// when every heavy job lands together.
//
// /rpc-cron answers 200 before the job runs (the work continues in a goroutine or on a
// RabbitMQ queue), so k6's latency numbers mean nothing here. Measure the worker's CPU
// and memory, Postgres load, RabbitMQ queue depth, and the "cron: ... done" log lines.
//
// These crons act on every tenant in the environment, as their schedules already do,
// unless noted. Run the worker test only where that is acceptable.
//
//   CRONS        comma-separated subset of SAFE_CRONS (default: all that can run)
//   ACCOUNT_IDS  comma-separated account ids; scopes the recommendation crons and is
//                required for K8s Recommendation refresh
//   REPEAT       times each cron fires (default 1)
//   INTERVAL     seconds between repeats of the same cron (default 60)
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { env, baseUrl } from './lib.js';

// Only jobs that recompute state. Everything else in api/cron.go was left out on purpose:
// digests, alerts, reports and batched notifications email or message real users; the
// cleanups and sweeps delete data; pr-lifecycle-check comments on GitHub; Bill* bills;
// Load Agent Playbook pushes to agents; Identity Sync and Resource Meta refresh call
// third-party APIs; Anomaly/SLO runs raise events that fan out to notifications.
// Check a cron's handler for side effects before adding it here.
const accountIds = env('ACCOUNT_IDS', '')
  .split(',')
  .map((id) => id.trim())
  .filter(Boolean);
const scoped = (payload) => (accountIds.length ? { ...payload, account_id: accountIds } : payload);

const SAFE_CRONS = {
  // Publishes one kg_update message per tenant, consumed by the worker's KG queue.
  build_knowledge_graph: () => ({ action: { name: 'build_knowledge_graphs_with_db_filters' }, input: { request: {} } }),
  // Force-rebuilds the KG for TENANT_ID only.
  'Knowledge Graph Refresh': () => ({ tenant_id: env('TENANT_ID') }),
  // Also dispatches popeye, trivy, kube-bench and helm scan Jobs to each account's agent,
  // so it only runs against the accounts in ACCOUNT_IDS.
  'K8s Recommendation refresh': () => scoped({}),
  // Reads each account's metrics through the relay.
  'Vertical Rightsizing Refresh': () => scoped({ batch_by_namespace: true, persist_recommendation: true }),
  // Cannot be scoped: re-closes stale insights across all tenants, as its 10-minute schedule does.
  'Insight refresh': () => ({}),
  'finops-score-recompute': () => ({}),
};
const NEEDS_ACCOUNT_IDS = ['K8s Recommendation refresh'];

const url = baseUrl('SERVICES_SERVER_WORKER_URL', 'http://services-server-worker:8000');
const runnable = Object.keys(SAFE_CRONS).filter((c) => accountIds.length || !NEEDS_ACCOUNT_IDS.includes(c));
const crons = env('CRONS', runnable.join(','))
  .split(',')
  .map((c) => c.trim())
  .filter(Boolean);
for (const c of crons) {
  if (!SAFE_CRONS[c]) throw new Error(`cron "${c}" is not in SAFE_CRONS; read its handler in api/cron.go before adding it`);
  if (!accountIds.length && NEEDS_ACCOUNT_IDS.includes(c)) throw new Error(`cron "${c}" needs ACCOUNT_IDS: unscoped, it scans every cluster`);
}
const repeat = Number(env('REPEAT', '1'));
const interval = Number(env('INTERVAL', '60'));
const headers = { 'Content-Type': 'application/json', 'X-ACTION-TOKEN': env('ACTION_API_SERVER_TOKEN', '') };

// One VU per cron, so every cron fires at the same moment on each round.
export const options = {
  scenarios: {
    cron_storm: { executor: 'per-vu-iterations', vus: crons.length, iterations: repeat, maxDuration: `${repeat * interval + 120}s` },
  },
  thresholds: { http_req_failed: ['rate==0'] },
};

export default function () {
  const name = crons[exec.vu.idInTest - 1];
  const body = JSON.stringify({ id: `loadtest-${exec.vu.idInTest}-${exec.vu.iterationInScenario}`, name, comment: 'k6 load test', payload: SAFE_CRONS[name]() });
  const res = http.post(`${url}/rpc-cron`, body, { headers, tags: { name } });
  check(res, { 'accepted (200)': (r) => r.status === 200 });
  if (exec.vu.iterationInScenario < repeat - 1) sleep(interval);
}
