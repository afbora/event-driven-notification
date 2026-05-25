# Load tests

[k6](https://k6.io) scenarios that exercise the running stack from the
outside, the same way an external client would. They are not part of
`make test` because they need a live api service and they take
minutes to finish — run them deliberately, observe the dashboards.

## Bring the stack up first

The load scenarios run against the api/worker via the loadtest
overlay (`docker-compose.loadtest.yml`). That overlay raises the
api's inbound rate limit from the production default of 60 req/min
to 100 000 req/min so the inbound limiter never becomes the gate —
otherwise the first 60 requests pass and the rest see 429, which
hides whatever the load script is actually trying to measure. The
*outbound* limit (100 msg/sec per channel) is **not** raised; the
rate-limit scenario depends on it being in force.

```bash
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
  up -d
make migrate-up           # if you haven't already
```

Running without the overlay (`docker compose up -d` only) leaves the
60 req/min inbound cap in place — fine for ad-hoc manual probing,
useless for k6 scenarios.

## Run a scenario

```bash
make load-test-baseline      # 300 rps for 60s — task 21
make load-test-burst         # 1000 rps spike + drain — task 22
make load-test-rate-limit    # 200 rps to one channel — task 23

make load-test               # all three, in sequence
```

Under the hood each target invokes:

```bash
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
  run --rm k6 run /scripts/<scenario>.js
```

The k6 container joins the same network as `api`, so it hits
`http://api:8080` directly — no host-port mapping, no public exposure.
The same overlay that defines the k6 service also raises the api's
inbound rate limit so the scenario's request rate is not throttled
at the edge (see the section above).

## Override the target URL

```bash
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml \
  run --rm -e BASE_URL=http://api:8080 k6 run /scripts/baseline.js
```

`BASE_URL` defaults to `http://api:8080` inside the compose network. A
developer running k6 outside the compose stack should set
`BASE_URL=http://localhost:8080`.

## Results & dashboards

- Each scenario prints a one-line summary to stdout.
- Live metrics: `make load-test-baseline` while watching Grafana
  (`http://localhost:3000`) — every k6 request creates a span the
  `notifications_*` collectors observe.
- Detailed results, methodology, and limitations live in
  `docs/LOAD_TEST.md`.
