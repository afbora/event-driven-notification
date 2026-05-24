// Package main runs the reconciler binary — the safety net that
// sweeps stuck notifications back into circulation (CLAUDE.md §3.11,
// ADR-0011). Runs on a fixed interval (default 1 minute) until
// SIGTERM. Multiple instances can run in parallel; the underlying
// queries use FOR UPDATE SKIP LOCKED so they never step on each other.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	hibikenasynq "github.com/hibiken/asynq"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/infrastructure/clock"
	"github.com/afbora/event-driven-notification/internal/infrastructure/config"
	"github.com/afbora/event-driven-notification/internal/infrastructure/id"
)

func main() {
	if err := run(); err != nil {
		slog.Error("reconciler exited with error", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	configureLogger(cfg.LogLevel)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer pool.Close()

	notifRepo := pgadapter.NewNotificationRepository(pool)
	logRepo := pgadapter.NewNotificationLogRepository(pool)

	queue := asynqadapter.NewQueue(hibikenasynq.RedisClientOpt{Addr: cfg.RedisAddr})
	defer func() { _ = queue.Close() }()

	uc := application.NewReconcileStuckNotifications(
		notifRepo, logRepo, queue,
		id.New(), clock.New(),
	)

	ticker := time.NewTicker(cfg.ReconcilerInterval)
	defer ticker.Stop()

	slog.Info("reconciler started", "interval", cfg.ReconcilerInterval.String())

	// Run one pass immediately on startup so the loop does not wait a
	// full interval before doing anything useful (the typical
	// scenario is "operator restarted the pod, please catch up").
	runOnce(rootCtx, uc)

	for {
		select {
		case <-rootCtx.Done():
			slog.Info("reconciler stopped cleanly")
			return nil
		case <-ticker.C:
			runOnce(rootCtx, uc)
		}
	}
}

// runOnce executes a single reconciliation pass. Errors are logged
// and swallowed — the loop keeps ticking so a transient failure does
// not park the safety net.
func runOnce(ctx context.Context, uc *application.ReconcileStuckNotifications) {
	out, err := uc.Execute(ctx, application.ReconcileStuckNotificationsInput{})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("reconciler pass failed", "error", err.Error())
		return
	}
	slog.Info("reconciler pass complete",
		"stuck_processing_failed", out.StuckProcessingFailed,
		"overdue_retrying_reenqueued", out.OverdueRetryingReenqueued,
		"orphaned_pending_reenqueued", out.OrphanedPendingReenqueued,
	)
}

func configureLogger(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(h))
}
