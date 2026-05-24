# Makefile — common project commands.
#
# Targets are organised by category; run `make help` for a summary.
# Most targets assume a Unix-like shell (Linux, macOS, WSL2, or Git Bash).
# Required tools beyond the Go toolchain are listed in `make tools`.

# --- Configuration ---------------------------------------------------------

BIN_DIR        := bin
BINARIES       := api worker reconciler migrate
COVERAGE_DIR   := coverage
COVERAGE_OUT   := $(COVERAGE_DIR)/coverage.out
COVERAGE_UNIT  := $(COVERAGE_DIR)/unit.out
COVERAGE_INT   := $(COVERAGE_DIR)/integration.out
COVERAGE_E2E   := $(COVERAGE_DIR)/e2e.out
MIGRATIONS_DIR := db/migrations

# DSN used by `make migrate-*`. Matches the docker-compose default for the
# postgres service; override per-call for ad-hoc targets, e.g.:
#   make migrate-up DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
DATABASE_URL   ?= postgres://notification:notification@localhost:5432/notification?sslmode=disable

.DEFAULT_GOAL := help

.PHONY: help \
        build clean \
        test test-race test-integration test-e2e \
        coverage coverage-unit coverage-integration coverage-e2e coverage-all coverage-html \
        load-test load-test-baseline load-test-burst load-test-rate-limit \
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
	rm -rf $(BIN_DIR) $(COVERAGE_DIR) coverage.html

# --- Test ------------------------------------------------------------------

test: ## Run unit tests across all packages.
	go test ./...

test-race: ## Run unit tests with the race detector enabled.
	go test -race ./...

test-integration: ## Run integration tests (//go:build integration) — adapter packages.
	go test -tags=integration ./internal/adapters/...

test-e2e: ## Run end-to-end tests (//go:build e2e).
	go test -tags=e2e ./tests/e2e/...

# --- Coverage --------------------------------------------------------------
# Each suite writes its own profile under $(COVERAGE_DIR); coverage-all
# merges them via gocovmerge so SonarCloud / `go tool cover` see one file.
# atomic mode is used everywhere so the merge is safe and race-aware.

$(COVERAGE_DIR):
	@mkdir -p $(COVERAGE_DIR)

coverage-unit: $(COVERAGE_DIR) ## Run unit tests with coverage → $(COVERAGE_UNIT).
	go test -covermode=atomic -coverpkg=./... -coverprofile=$(COVERAGE_UNIT) ./...

coverage-integration: $(COVERAGE_DIR) ## Run integration tests with coverage → $(COVERAGE_INT).
	go test -tags=integration -covermode=atomic -coverpkg=./... -coverprofile=$(COVERAGE_INT) ./internal/adapters/...

coverage-e2e: $(COVERAGE_DIR) ## Run end-to-end tests with coverage → $(COVERAGE_E2E).
	go test -tags=e2e -covermode=atomic -coverpkg=./... -coverprofile=$(COVERAGE_E2E) ./tests/e2e/...

coverage-all: coverage-unit coverage-integration coverage-e2e ## Run every suite then merge into $(COVERAGE_OUT) via gocovmerge.
	gocovmerge $(COVERAGE_UNIT) $(COVERAGE_INT) $(COVERAGE_E2E) > $(COVERAGE_OUT)
	@echo "merged coverage written to $(COVERAGE_OUT)"

coverage-html: $(COVERAGE_OUT) ## Render coverage.out as coverage.html.
	go tool cover -html=$(COVERAGE_OUT) -o coverage.html
	@echo "open coverage.html in a browser"

coverage: coverage-unit ## Shorthand for unit-only coverage; use coverage-all for everything.
	go tool cover -html=$(COVERAGE_UNIT) -o coverage.html
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

tools: ## Install required Go tools into $GOBIN: lint, sqlc, migrate, oapi-codegen, air, gocovmerge.
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	go install github.com/air-verse/air@latest
	go install github.com/wadey/gocovmerge@latest

# --- Run locally (without docker compose) ----------------------------------

run-api: ## Run the api binary directly via `go run` against local services.
	go run ./cmd/api

run-worker: ## Run the worker binary directly via `go run` against local services.
	go run ./cmd/worker

run-reconciler: ## Run the reconciler binary directly via `go run` against local services.
	go run ./cmd/reconciler

# --- Load tests ------------------------------------------------------------
# k6 runs in its own container against the api service. All three
# scenarios require the main compose stack to be up first
# (`docker compose up -d`).

LOADTEST_COMPOSE := docker compose -f docker-compose.yml -f docker-compose.loadtest.yml --profile loadtest

load-test-baseline: ## k6 baseline scenario — 300 rps for 60s.
	$(LOADTEST_COMPOSE) run --rm k6 run /scripts/baseline.js

load-test-burst: ## k6 burst scenario — 1000 rps for 10s, then idle 50s.
	$(LOADTEST_COMPOSE) run --rm k6 run /scripts/burst.js

load-test-rate-limit: ## k6 rate-limit scenario — 200 rps to a single channel.
	$(LOADTEST_COMPOSE) run --rm k6 run /scripts/rate_limit.js

load-test: load-test-baseline load-test-burst load-test-rate-limit ## Run every k6 scenario in sequence.
