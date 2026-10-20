// llm-server chat, driven the way the Ask Nudgebee UI drives it: an async POST to
// /v1/completions/chat (202 Accepted), then polling the conversation through
// services-server's ai_get_conversation_detail_polling action until it finishes.
// One iteration is one conversation, so RATE is conversations started per minute
// (TIME_UNIT defaults to 1m here); smoke starts a single conversation.
//
// conversation_duration (submit -> terminal status) is the number that matters;
// http_req_duration only covers the submit and poll calls. A 503 on submit means
// llm-server's async worker pool is full.
//
// Point the tenant's LLM config at the mock gateway first (README), or every
// iteration spends real model tokens and runs real tools.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { env, baseUrl, scenarios, thresholds } from './lib.js';

const llmUrl = baseUrl('LLM_SERVER_URL', 'http://llm-server:8000');
const servicesUrl = baseUrl('SERVICES_SERVER_URL', 'http://services-server:8000');
const tenantId = env('TENANT_ID');
const accountId = env('ACCOUNT_ID');
const query = env('QUERY', 'List the namespaces in this cluster.');
const pollEvery = Number(env('POLL_INTERVAL', '2'));
const maxWait = Number(env('MAX_WAIT', '300'));

const llmHeaders = {
  'Content-Type': 'application/json',
  [env('LLM_SERVER_TOKEN_HEADER', 'X-ACTION-TOKEN')]: env('LLM_SERVER_TOKEN', ''),
  'x-tenant-id': tenantId,
};
const servicesHeaders = {
  'Content-Type': 'application/json',
  'X-ACTION-TOKEN': env('ACTION_API_SERVER_TOKEN', ''),
  'x-tenant-id': tenantId,
};

const conversationDuration = new Trend('conversation_duration', true);
const outcomes = new Counter('conversation_outcomes');

const RUNNING = new Set(['IN_PROGRESS', 'PENDING', 'QUEUED']);
const NEEDS_INPUT = new Set(['WAITING', 'WAITING_FOR_CLIENT_TOOL']);

export const options = {
  scenarios: scenarios('run', { timeUnit: '1m', gracefulStop: `${maxWait + pollEvery}s` }),
  thresholds: {
    ...thresholds(2000),
    conversation_duration: [`p(95)<${env('CONVERSATION_P95_MS', '60000')}`],
    // Per outcome, because the many successful polls per conversation would dilute a
    // failed submit below http_req_failed's 1% line.
    ...Object.fromEntries(
      ['failed', 'timeout', 'rejected_pool_full', 'submit_error', 'poll_error'].map((o) => [`conversation_outcomes{outcome:${o}}`, ['count==0']]),
    ),
  },
};

function uuidv4() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function pollStatus(sessionId) {
  const body = JSON.stringify({
    action: { name: 'ai_get_conversation_detail_polling' },
    input: { columns: ['id', 'status'], where: { session_id: { _eq: sessionId } } },
    request_query: '',
    session_variables: { role: 'tenant_admin', tenant_id: tenantId },
  });
  const res = http.post(`${servicesUrl}/rpc/query`, body, { headers: servicesHeaders, tags: { name: 'ai_get_conversation_detail_polling' } });
  if (res.status !== 200) return { error: true };
  const rows = res.json('rows') || [];
  // No row yet means the async worker has not picked the conversation up.
  return { status: rows.length ? rows[0].status : undefined };
}

export function run() {
  const sessionId = uuidv4();
  const started = Date.now();
  const submit = http.post(
    `${llmUrl}/v1/completions/chat`,
    JSON.stringify({ input: { request: { account_id: accountId, query, session_id: sessionId, async: true } } }),
    { headers: llmHeaders, tags: { name: 'chat_submit' } },
  );
  if (!check(submit, { 'submit accepted (202)': (r) => r.status === 202 })) {
    outcomes.add(1, { outcome: submit.status === 503 ? 'rejected_pool_full' : 'submit_error' });
    return;
  }

  let status;
  while ((Date.now() - started) / 1000 < maxWait) {
    sleep(pollEvery);
    const poll = pollStatus(sessionId);
    // A failed poll (bad token, services-server erroring) ends this conversation's
    // measurement instead of retrying for MAX_WAIT.
    if (poll.error) {
      outcomes.add(1, { outcome: 'poll_error' });
      return;
    }
    status = poll.status;
    if (status && !RUNNING.has(status)) break;
  }

  let outcome = 'failed';
  if (!status || RUNNING.has(status)) outcome = 'timeout';
  else if (status === 'COMPLETED') outcome = 'completed';
  else if (NEEDS_INPUT.has(status)) outcome = 'needs_input';
  outcomes.add(1, { outcome });
  if (outcome === 'completed' || outcome === 'needs_input') conversationDuration.add(Date.now() - started);
}
