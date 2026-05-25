// Package main runs the migrate binary — a thin CLI wrapper around
// golang-migrate that points at the project's db/migrations directory
// and the DATABASE_URL environment variable.
//
// Usage:
//
//	migrate up                  # apply every pending migration
//	migrate down [steps]        # roll back `steps` migrations (default 1)
//	migrate version             # print the current schema version
//	migrate force <version>     # set the schema version without running migrations
//
// The binary is meant to run from the same image as the api/worker so
// CI and ops automation reuse the artifact (CLAUDE.md §5 — one image,
// three binaries).
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // register pg driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // register file source

	"github.com/afbora/event-driven-notification/internal/infrastructure/config"
)

const migrationsPath = "file://db/migrations"

func main() {
	if err := run(); err != nil {
		slog.Error("migrate failed", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(os.Args) < 2 {
		return errors.New("usage: migrate <up|down|version|force> [args]")
	}

	m, err := migrate.New(migrationsPath, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open migrate: %w", err)
	}
	defer func() {
		// migrate.Close returns two errors (source + db); we ignore
		// them on shutdown — anything they would report is incidental
		// after the actual migration command finished.
		_, _ = m.Close()
	}()

	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "up":
		return runUp(m)
	case "down":
		return runDown(m, args)
	case "version":
		return runVersion(m)
	case "force":
		return runForce(m, args)
	default:
		return fmt.Errorf("unknown command %q (use up|down|version|force)", cmd)
	}
}

// runUp applies every pending migration. migrate.ErrNoChange is not
// an error from a CLI standpoint — it means "already at HEAD."
func runUp(m *migrate.Migrate) error {
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("up: %w", err)
	}
	slog.Info("migrations applied")
	return nil
}

// runDown rolls back N migrations. N defaults to 1; when supplied it
// must be a positive integer.
func runDown(m *migrate.Migrate, args []string) error {
	steps := 1
	if len(args) >= 1 {
		n, perr := strconv.Atoi(args[0])
		if perr != nil || n < 1 {
			return fmt.Errorf("down steps must be a positive integer, got %q", args[0])
		}
		steps = n
	}
	if err := m.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("down %d: %w", steps, err)
	}
	slog.Info("migrations rolled back", "steps", steps)
	return nil
}

// runVersion prints the current schema version. migrate.ErrNilVersion
// (no migrations applied yet) is not an error; we just print version=0.
func runVersion(m *migrate.Migrate) error {
	v, dirty, verr := m.Version()
	if verr != nil && !errors.Is(verr, migrate.ErrNilVersion) {
		return fmt.Errorf("version: %w", verr)
	}
	fmt.Printf("version=%d dirty=%v\n", v, dirty)
	return nil
}

// runForce sets the schema version pointer without running any
// migration. Used to recover from a dirty state.
func runForce(m *migrate.Migrate, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: migrate force <version>")
	}
	v, perr := strconv.Atoi(args[0])
	if perr != nil {
		return fmt.Errorf("force version must be an integer, got %q", args[0])
	}
	if err := m.Force(v); err != nil {
		return fmt.Errorf("force %d: %w", v, err)
	}
	slog.Info("migration version forced", "version", v)
	return nil
}
