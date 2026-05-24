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

	switch cmd := os.Args[1]; cmd {
	case "up":
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("up: %w", err)
		}
		slog.Info("migrations applied")
	case "down":
		steps := 1
		if len(os.Args) >= 3 {
			n, perr := strconv.Atoi(os.Args[2])
			if perr != nil || n < 1 {
				return fmt.Errorf("down steps must be a positive integer, got %q", os.Args[2])
			}
			steps = n
		}
		if err := m.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("down %d: %w", steps, err)
		}
		slog.Info("migrations rolled back", "steps", steps)
	case "version":
		v, dirty, verr := m.Version()
		if verr != nil && !errors.Is(verr, migrate.ErrNilVersion) {
			return fmt.Errorf("version: %w", verr)
		}
		fmt.Printf("version=%d dirty=%v\n", v, dirty)
	case "force":
		if len(os.Args) < 3 {
			return errors.New("usage: migrate force <version>")
		}
		v, perr := strconv.Atoi(os.Args[2])
		if perr != nil {
			return fmt.Errorf("force version must be an integer, got %q", os.Args[2])
		}
		if err := m.Force(v); err != nil {
			return fmt.Errorf("force %d: %w", v, err)
		}
		slog.Info("migration version forced", "version", v)
	default:
		return fmt.Errorf("unknown command %q (use up|down|version|force)", cmd)
	}
	return nil
}
