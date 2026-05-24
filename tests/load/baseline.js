// baseline.js — sustained throughput scenario.
//
// Goal (PLAN.md phase 5 task 21): 300 req/sec for 60 s, asserting
// the api accepts traffic without DLQ growth and keeps p95 accept
// latency under 200 ms. 300 rps ≈ 3 channels × the 100/sec outbound
// cap, so the worker fleet should drain in real time.
//
// Run via docker compose:
//   docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
//     run --rm k6 run /scripts/baseline.js

import {
  BASE_URL,
  postNotification,
  expectAccepted,
} from './helpers.js';

export const options = {
  scenarios: {
    baseline: {
      executor: 'constant-arrival-rate',
      rate: 300,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 40,
      maxVUs: 200,
    },
  },
  thresholds: {
    'http_req_duration{expected_response:true}': ['p(95)<200'],
    'http_req_failed': ['rate<0.01'],
    'checks': ['rate>0.99'],
  },
};

const recipients = (() => {
  const out = [];
  for (let i = 0; i < 1000; i++) {
    out.push(`+1555555${String(1000 + i).padStart(4, '0')}`);
  }
  return out;
})();

export default function () {
  const recipient = recipients[Math.floor(Math.random() * recipients.length)];
  const res = postNotification({ recipient });
  expectAccepted(res, 'baseline');
}

export function handleSummary(data) {
  // Surface a one-line summary so the developer running locally can
  // eyeball the result even without Grafana attached.
  return {
    stdout: `baseline complete: base_url=${BASE_URL} requests=${data.metrics.http_reqs.values.count} p95=${data.metrics.http_req_duration.values['p(95)'].toFixed(1)}ms\n`,
  };
}
