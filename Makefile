# Makefile — common project commands.
#
# Targets are organised by category; run `make help` for a summary.
# Most targets assume a Unix-like shell (Linux, macOS, WSL2, or Git Bash).
# Required tools beyond the Go toolchain are listed in `make tools`.

# --- Configuration ---------------------------------------------------------

BIN_DIR        := bin
BINARIES       := api worker reconciler migrate
COVERAGE_OUT   := coverage.out
MIGRATIONS_DIR := db/migrations

# DSN used by `make migrate-*`. Matches the docker-compose default for the
# postgres service; override per-call for ad-hoc targets, e.g.:
#   make migrate-up DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
DATABASE_URL   ?= postgres://notification:notification@localhost:5432/notification?sslmode=disable

.DEFAULT_GOAL := help

.PHONY: help \
        build clean \
        test test-race test-integration test-e2e coverage \
        lint lint-fix \
        sqlc openapi \
        migrate-up migrate-down migrate-create \
        tools \
        run-api run-worker run-reconciler

# --- Help ------------------------------------------------------------------

help: ## List available targets.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# --- Build -----------------------------------------------------------------

build: ## Compile every cmd/* binary into ./bin.
	@mkdir -p $(BIN_DIR)
	@for binary in $(BINARIES); do \
		echo "==> building $$binary"; \
		go build -trimpath -o $(BIN_DIR)/$$binary ./cmd/$$binary || exit 1; \
	done

clean: ## Remove build artifacts and coverage outputs.
	rm -rf $(BIN_DIR) $(COVERAGE_OUT) coverage.html

# --- Test ------------------------------------------------------------------

test: ## Run unit tests across all packages.
	go test ./...

test-race: ## Run unit tests with the race detector enabled.
	go test -race ./...

test-integration: ## Run integration tests (//go:build integration).
	go test -tags=integration ./tests/integration/...

test-e2e: ## Run end-to-end tests (//go:build e2e).
	go test -tags=e2e ./tests/e2e/...

coverage: ## Run tests with coverage and emit coverage.out + coverage.html.
	go test -coverprofile=$(COVERAGE_OUT) ./...
	go tool cover -html=$(COVERAGE_OUT) -o coverage.html
	@echo "open coverage.html in a browser"

# --- Lint ------------------------------------------------------------------

lint: ## Run golangci-lint across all packages.
	golangci-lint run ./...

lint-fix: ## Run golangci-lint with --fix for auto-fixable findings.
	golangci-lint run --fix ./...

# --- Code generation -------------------------------------------------------

sqlc: ## Regenerate sqlc bindings (config lives under internal/adapters/postgres).
	sqlc generate

openapi: ## Regenerate the HTTP server interface from api/openapi.yaml.
	oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
	cp api/openapi.yaml internal/adapters/http/openapi.yaml.embed

# --- Database migrations ---------------------------------------------------

migrate-up: ## Apply all pending migrations against $DATABASE_URL.
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS_DIR) up

migrate-down: ## Roll back the most recent migration against $DATABASE_URL.
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS_DIR) down 1

migrate-create: ## Create a new migration pair. Usage: make migrate-create NAME=short_description
	@if [ -z "$(NAME)" ]; then \
		echo "NAME is required: make migrate-create NAME=<short_description>"; \
		exit 1; \
	fi
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(NAME)

# --- Dev tools -------------------------------------------------------------

tools: ## Install required Go tools into $GOBIN: lint, sqlc, migrate, oapi-codegen, air.
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	go install github.com/air-verse/air@latest

# --- Run locally (without docker compose) ----------------------------------

run-api: ## Run the api binary directly via `go run` against local services.
	go run ./cmd/api

run-worker: ## Run the worker binary directly via `go run` against local services.
	go run ./cmd/worker

run-reconciler: ## Run the reconciler binary directly via `go run` against local services.
	go run ./cmd/reconciler
