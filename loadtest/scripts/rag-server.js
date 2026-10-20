// rag-server retrieval, called the way llm-server calls it during a conversation:
// POST /get_matching_doc (embed the query, vector search, rerank).
//
// rag-server caches embeddings, so a single repeated query measures the cache. The
// default rotates through varied questions; UNIQUE_QUERIES=true makes every query
// unique for the uncached worst case. The reranker model loads on first use, so the
// first requests after a pod start are slow; run a smoke pass before measuring.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { env, baseUrl, scenarios, thresholds } from './lib.js';

const url = baseUrl('RAG_SERVER_URL', 'http://rag-server:9999');
const accountId = env('ACCOUNT_ID');
const modules = env('MODULES', 'knowledge_base').split(',');
const k = Number(env('K', '3'));
const unique = env('UNIQUE_QUERIES', 'false') === 'true';
const headers = { 'Content-Type': 'application/json', 'X-ACTION-TOKEN': env('RAG_SERVER_TOKEN', '') };

const QUERIES = [
  'pod is in CrashLoopBackOff after a deployment',
  'container killed with OOMKilled exit code 137',
  'high memory usage on the database pod',
  'how do I restart a stuck deployment',
  'node disk pressure evicting pods',
  'service returns 502 from the ingress',
  'postgres connection pool exhausted',
  'kafka consumer lag is growing',
  'certificate expired on the load balancer',
  'CPU throttling on the api pods',
  'runbook for rotating database credentials',
  'readiness probe failing after upgrade',
];

export const options = {
  scenarios: scenarios('run'),
  thresholds: thresholds(3000),
};

export function run() {
  const i = exec.scenario.iterationInTest;
  let query = QUERIES[i % QUERIES.length];
  if (unique) query = `${query} (${i})`;
  const module = modules[i % modules.length];
  const body = JSON.stringify({ account_id: accountId, query, module, k, conversation_id: '' });
  const res = http.post(`${url}/get_matching_doc`, body, { headers, tags: { name: `get_matching_doc:${module}` } });
  check(res, { 'status 200': (r) => r.status === 200 });
}
