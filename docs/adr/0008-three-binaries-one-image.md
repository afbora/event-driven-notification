# ADR-0008: Three Binaries (api, worker, reconciler) Packaged In One Image

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Ahmet Bora

## Context

The system has three runtime workloads with very different scaling characteristics:

- **API server** — stateless, scales horizontally with incoming HTTP traffic.
- **Worker** — stateless, scales horizontally with queue depth.
- **Reconciler** — stateless, but only one instance does meaningful work most of the time (others coordinate via `SELECT ... FOR UPDATE SKIP LOCKED` per CLAUDE.md §3.11).

Plus a fourth, run-once workload:

- **Migrate** — applies schema migrations at deploy time.

Each is a separate `package main` under `cmd/`. The packaging question is whether to build separate Docker images per workload, or one image that contains all four binaries and dispatches by command argument.

The forces:

- Production deployments (Kubernetes, ECS, Nomad) scale each tier independently — different replica counts for `api` vs `worker`. The deployment shape needs to be independent regardless of image packaging.
- Image build time, registry storage, and image-pull bandwidth multiply with separate images.
- Code is shared: every binary depends on `internal/domain/`, `internal/application/`, `internal/ports/`, `internal/adapters/`, `internal/infrastructure/`. Separate builds compile the same code four times.

## Decision

The repository builds **one** Docker image. The image contains all four binaries at `/usr/local/bin/{api,worker,reconciler,migrate}`. The active workload is selected at runtime by the container's command:

```bash
docker run --rm myimage          # default → api
docker run --rm myimage worker
docker run --rm myimage reconciler
docker run --rm myimage migrate
```

In `docker-compose.yml`, each service references the same image and overrides the command:

```yaml
api:
  build: .
  command: ["air", "-c", ".air.api.toml"]   # dev hot reload via Dockerfile.dev

worker:
  build: .
  command: ["air", "-c", ".air.worker.toml"]
```

In Kubernetes (out of scope for this assessment, but worth noting in this ADR), each workload would be a separate Deployment / Job / CronJob, all referencing the same `Image` field, each with a different `args:` array.

## Consequences

**Positive:**

- One Dockerfile to maintain, one image to scan for vulnerabilities, one image to push to a registry, one set of base-image updates to track.
- Internal packages compile once per image build, not four times.
- Operational symmetry: the same shell command that runs the api also runs the worker, just with a different command argument.
- Deploy ergonomics: a single `docker push` makes all four binaries available simultaneously.

**Negative:**

- The image is slightly larger than a single-binary image would be. With distroless/static + four ~5MB Go binaries (after `-ldflags="-s -w"`), the total is about 25–30 MB. Negligible in practice.
- Container security policies that mount the binary directory read-only must allow all four executables. Standard practice; not a real downside.
- A reviewer skim might mistake the image for "monolith" because all four binaries are present. The image is monolithic; the **process** is not — each container runs exactly one binary, and they scale independently.

## Alternatives Considered

1. **One image per binary** — rejected. Four Dockerfiles, four CI build jobs, four times the registry storage. The only benefit would be a marginal image-size reduction that doesn't matter at this scale.
2. **One binary with a `--mode=worker` flag** — rejected. Makes the binary larger (it has to link every codepath), couples HTTP/queue/reconciler concerns into one entrypoint, and complicates initialisation (the binary now needs to know not to wire HTTP if `--mode=worker`). The compile-time separation of `cmd/api/main.go` vs `cmd/worker/main.go` is clearer.
3. **Three separate binaries with three separate Dockerfiles all sharing a common `internal/`** — rejected. Same multi-image overhead as #1 plus the build context complexity of sharing source between Dockerfiles.

## Related

- CLAUDE.md §5 (Architecture — Three binaries, one image)
- `Dockerfile` (production multi-stage build)
- `Dockerfile.dev` (development image with `air`)
- `docker-compose.yml` (per-service command override)
