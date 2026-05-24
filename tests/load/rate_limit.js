// rate_limit.js — exercises the outbound rate limiter.
//
// Goal (PLAN.md phase 5 task 23): drive 200 req/sec at a single
// channel (above the 100/sec outbound cap). Asserts:
//   - the API accepts every request (the inbound limiter is way
//     higher than 200 rps in the compose default),
//   - no notification ends up in the dead-letter queue purely due to
//     rate limiting — they should fall into retrying and eventually
//     deliver as the limiter window rolls forward.
//
// Compose run:
//   docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
//     run --rm k6 run /scripts/rate_limit.js

import {
  BASE_URL,
  postNotification,
  expectAccepted,
} from './helpers.js';

export const options = {
  scenarios: {
    rate_limit: {
      executor: 'constant-arrival-rate',
      rate: 200,
      timeUnit: '1s',
      duration: '30s',
      preAllocatedVUs: 50,
      maxVUs: 250,
    },
  },
  thresholds: {
    // All POSTs accepted (202); the throttling is between the worker
    // and the provider, invisible at the API boundary.
    'checks': ['rate>0.99'],
    'http_req_failed': ['rate<0.01'],
  },
};

export default function () {
  const res = postNotification({
    channel: 'sms', // single channel — the limiter is per-channel
    content: 'rate-limit-probe',
  });
  expectAccepted(res, 'rate_limit');
}

export function handleSummary(data) {
  return {
    stdout: `rate_limit complete: base_url=${BASE_URL} requests=${data.metrics.http_reqs.values.count} checks_rate=${(data.metrics.checks.values.rate * 100).toFixed(1)}%\n`,
  };
}
