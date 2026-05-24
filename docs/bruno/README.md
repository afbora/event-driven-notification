# Bruno API collection

This directory is a [Bruno](https://www.usebruno.com/) collection that
covers every endpoint declared in `api/openapi.yaml`. Bruno was chosen
over Postman because the `.bru` format is plain text, lives in git
alongside the spec, and needs no cloud account.

## Use it

- **Desktop**: open Bruno → *Open Collection* → point at this folder.
- **CLI**: from the project root, run `bru run docs/bruno -r --env Local`
  to execute every request in sequence against a running compose stack.

## Layout

```
docs/bruno/
├── bruno.json                — collection metadata
├── environments/
│   └── Local.bru            — BASE_URL + sample identifiers
├── Notifications/            — create / list / get / cancel / trace
├── Batches/                  — create / get
├── Templates/                — create / list / get / replace / delete
└── Meta/                     — healthz, /metrics, /api/v1/metrics
```

## Conventions

- `{{BASE_URL}}` points at the api service (default `http://localhost:8080`).
- `{{idempotencyKey}}` / `{{correlationId}}` are sample values; replace
  with your own when testing real flows.
- Request bodies use realistic payloads so you can `bru run` end-to-end
  against `docker compose up` without editing.
