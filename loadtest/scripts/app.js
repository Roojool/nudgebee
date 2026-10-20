// app end to end: JSON-RPC to /api/rpc with a personal API token, through the in-app
// gateway (auth, role check, upstream routing) to services-server. Uses the same action
// mix as services-server-api.js, so comparing the two runs isolates the gateway's cost.
//
// Create the token in the UI (Settings > API Tokens) for a user in the load-test tenant.
// The app rate-limits per client; run from inside the cluster (run.sh) and raise or
// exempt the limit for the test, or you will be measuring the limiter.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { env, baseUrl, scenarios, thresholds, weightedPicker } from './lib.js';

const actions = JSON.parse(open('./services-server-actions.json'));
const pick = weightedPicker(actions);

const url = baseUrl('APP_URL');
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${env('API_TOKEN')}` };

export const options = {
  scenarios: scenarios('run'),
  thresholds: thresholds(2000, actions.map((a) => a.name)),
};

export function run() {
  const a = pick();
  const body = JSON.stringify({ jsonrpc: '2.0', id: exec.scenario.iterationInTest, method: a.name, params: a.input });
  const res = http.post(`${url}/api/rpc`, body, { headers, tags: { name: a.name } });
  check(res, {
    'status 200': (r) => r.status === 200,
    'no rpc error': (r) => r.status !== 200 || !r.json('error'),
  });
}
