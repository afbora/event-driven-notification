# Load tests

[k6](https://k6.io) scenarios that exercise the running stack from the
outside, the same way an external client would. They are not part of
`make test` because they need a live api service and they take
minutes to finish — run them deliberately, observe the dashboards.

## Bring the stack up first

```bash
docker compose up -d
make migrate-up           # if you haven't already
```

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
