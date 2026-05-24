// helpers.js — shared utilities for every k6 scenario.
//
// Each scenario imports the bits it needs. We keep payloads small,
// avoid sharing mutable state across VUs, and prefer explicit headers
// so the assertions stay readable in the script bodies.

import http from 'k6/http';
import { check } from 'k6/check';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

// BASE_URL points at the api service. The compose runner injects
// http://api:8080; a developer running k6 outside the stack can
// override via -e BASE_URL=http://localhost:8080.
export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// jsonHeaders are the only headers most scenarios need. A unique
// X-Correlation-ID per request gives the operator a pivot when
// pulling logs back together.
export function jsonHeaders() {
  return {
    'Content-Type': 'application/json',
    'X-Correlation-ID': uuidv4(),
  };
}

// withIdempotencyKey returns the same headers plus a caller-supplied
// idempotency key. Scenarios that exercise the cache or retry path
// reach for this.
export function withIdempotencyKey(key) {
  return Object.assign(jsonHeaders(), { 'Idempotency-Key': key });
}

// postNotification fires a single POST /api/v1/notifications with a
// minimal SMS payload. Returns the parsed http.Response so the
// caller can inspect status / latency.
export function postNotification(opts) {
  opts = opts || {};
  const payload = JSON.stringify({
    channel: opts.channel || 'sms',
    recipient: opts.recipient || '+15555550000',
    content: opts.content || 'load-test',
    priority: opts.priority || 'normal',
  });
  const headers = opts.idempotencyKey
    ? withIdempotencyKey(opts.idempotencyKey)
    : jsonHeaders();
  return http.post(`${BASE_URL}/api/v1/notifications`, payload, { headers });
}

// expectAccepted asserts the POST returned 202 and stamps a single
// metric for the dashboard. Caller-supplied label disambiguates a
// scenario's checks when several share a script.
export function expectAccepted(res, label) {
  check(res, {
    [`${label}: 202 Accepted`]: (r) => r.status === 202,
  });
}

// expectThrottled asserts the response is the 429 the inbound limiter
// emits. Used by the rate-limit scenario to confirm protection
// engages under burst.
export function expectThrottled(res, label) {
  check(res, {
    [`${label}: 429 Too Many Requests`]: (r) => r.status === 429,
    [`${label}: has Retry-After`]: (r) => !!r.headers['Retry-After'],
  });
}
