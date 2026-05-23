# syntax=docker/dockerfile:1
#
# Multi-stage build for the notification system.
#
#   builder  -> golang:1.23-alpine; compiles every cmd/* binary statically.
#   runtime  -> gcr.io/distroless/static-debian12:nonroot; no shell, no apt,
#               runs as uid 65532. The same image hosts all four binaries
#               (api, worker, reconciler, migrate); pick the active one by
#               overriding the container command, e.g.
#                 docker run --rm myimage worker
#                 docker run --rm myimage migrate
#               The default is `api`.

ARG GO_VERSION=1.23

# --- Stage 1: builder -------------------------------------------------------

FROM golang:${GO_VERSION}-alpine AS builder

# git is occasionally needed by `go mod download` for VCS-backed deps;
# tzdata and ca-certificates are copied into the runtime image below.
RUN apk add --no-cache git tzdata ca-certificates

WORKDIR /src

# Cache module downloads in their own layer: invalidated only when go.mod
# (and later go.sum) change. Phase 1 has no external deps; go.sum lands here
# once the first test or runtime dependency is added.
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the rest of the source tree (.dockerignore strips local noise).
COPY . .

# Compile every cmd/* binary statically.
#   CGO_ENABLED=0          : no C runtime dependency, compatible with distroless/static.
#   -trimpath              : strip local filesystem paths from the binary.
#   -ldflags="-s -w"       : drop symbol table and DWARF info for a smaller image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    mkdir -p /out; \
    for binary in api worker reconciler migrate; do \
      CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/$binary \
        ./cmd/$binary; \
    done

# --- Stage 2: runtime -------------------------------------------------------

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# OCI image metadata. `image.source` lets `docker inspect` and any registry
# scanner trace the image back to this repository.
LABEL org.opencontainers.image.source="https://github.com/afbora/event-driven-notification"
LABEL org.opencontainers.image.licenses="MIT"

# Time-zone database and TLS root certificates from the builder, so we don't
# depend on distroless's bundled set staying in sync with our locale needs.
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# All four binaries — full paths in CMD so distroless's minimal $PATH resolves
# them predictably even if it changes upstream.
COPY --from=builder /out/api        /usr/local/bin/api
COPY --from=builder /out/worker     /usr/local/bin/worker
COPY --from=builder /out/reconciler /usr/local/bin/reconciler
COPY --from=builder /out/migrate    /usr/local/bin/migrate

# distroless `:nonroot` already runs as uid 65532; no USER directive needed.
CMD ["/usr/local/bin/api"]
