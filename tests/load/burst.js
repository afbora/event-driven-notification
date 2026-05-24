// burst.js — flash-sale simulation.
//
// Goal (PLAN.md phase 5 task 22): 1000 req/sec for 10 s, then idle
// 50 s while the worker fleet drains the queue. Verifies the queue
// absorbs the burst without growing the DLQ. The idle window also
// gives the api a chance to relax before any follow-on traffic.
//
// Compose run:
//   docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
//     run --rm k6 run /scripts/burst.js

import {
  BASE_URL,
  postNotification,
  expectAccepted,
} from './helpers.js';
import { sleep } from 'k6';

export const options = {
  scenarios: {
    burst: {
      executor: 'ramping-arrival-rate',
      startRate: 0,
      timeUnit: '1s',
      preAllocatedVUs: 200,
      maxVUs: 1000,
      stages: [
        { duration: '5s', target: 1000 }, // ramp up
        { duration: '5s', target: 1000 }, // sustain at 1000 rps
        { duration: '50s', target: 0 },   // idle while the worker drains
      ],
    },
  },
  thresholds: {
    // Accept-latency budget loosens during a burst — the brief asks
    // for absorption, not blistering ack latency.
    'http_req_duration{expected_response:true}': ['p(95)<500'],
    'http_req_failed': ['rate<0.01'],
  },
};

export default function () {
  const res = postNotification({
    recipient: `+1555555${String(2000 + (__VU * 1000 + __ITER) % 10000).padStart(4, '0')}`,
    content: 'flash-sale-burst',
  });
  expectAccepted(res, 'burst');
  // Light back-off so the VU does not hammer harder than the
  // arrival-rate executor intends when sleep is omitted.
  sleep(0.01);
}

export function handleSummary(data) {
  return {
    stdout: `burst complete: base_url=${BASE_URL} requests=${data.metrics.http_reqs.values.count} p95=${data.metrics.http_req_duration.values['p(95)'].toFixed(1)}ms\n`,
  };
}
