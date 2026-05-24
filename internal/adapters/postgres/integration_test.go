//go:build integration

// Package postgres_test holds the integration-tagged tests for the postgres
// adapter. They spin up a real Postgres 16 container per test (via
// testcontainers-go), apply every migration in db/migrations, and exercise
// the repository against that container.
//
// Run them with:
//
//	go test -tags=integration ./internal/adapters/postgres/...
//
// or `make test-integration` once the Makefile target is wired (PLAN.md
// phase 7 task 1).

package postgres_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/golang-migrate/migrate/v4"
	// Postgres database driver for golang-migrate (registers via init).
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	// File source driver for golang-migrate (registers via init).
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// setupPostgres spins up a fresh Postgres 16 container, applies every
// migration in db/migrations, and returns a pgx connection pool plus a
// cleanup function that tears the container down. Each test gets its own
// container, so cross-test state pollution is impossible — at the cost of
// container startup time per test (~3-5s).
//
// Tests that share one container should call setupPostgresShared and use
// truncateAll between subtests; this helper is for tests that exercise
// schema operations or want hermetic isolation.
func setupPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("notification"),
		postgres.WithUsername("notification"),
		postgres.WithPassword("notification"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "container connection string")

	applyMigrations(t, dsn)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "open pgx pool")

	cleanup := func() {
		pool.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	}
	return pool, cleanup
}

// applyMigrations runs every up migration in db/migrations against dsn.
// Resolves the migrations directory relative to this source file so the
// test works no matter where `go test` is invoked from.
func applyMigrations(t *testing.T, dsn string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "db", "migrations")

	sourceURL := "file://" + filepath.ToSlash(migrationsDir)
	m, err := migrate.New(sourceURL, dsn)
	require.NoError(t, err, "open migrate")

	if err := m.Up(); err != nil {
		require.ErrorIs(t, err, migrate.ErrNoChange, "apply migrations")
	}

	srcErr, dbErr := m.Close()
	require.NoError(t, srcErr, "migrate source close")
	require.NoError(t, dbErr, "migrate database close")
}

// truncateAll wipes every table back to empty between subtests in the same
// container. CASCADE handles the FK from notification_logs → notifications.
//
//nolint:unused // helper for upcoming repository tests
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`TRUNCATE notifications, notification_logs, batches, templates RESTART IDENTITY CASCADE`)
	require.NoError(t, err, "truncate all tables")
}

// TestIntegrationScaffolding is a smoke test for the scaffolding itself.
// It proves the container starts, the migrations apply, and the pool
// connects — so failures in later repository tests can be attributed to
// the repository code, not the scaffold.
func TestIntegrationScaffolding(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	var count int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('notifications', 'notification_logs', 'batches', 'templates')`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 4, count, "expected 4 application tables to be created by migrations")

	truncateAll(t, pool)
}
