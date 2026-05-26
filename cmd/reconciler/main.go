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
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	hibikenasynq "github.com/hibiken/asynq"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/infrastructure/clock"
	"github.com/afbora/event-driven-notification/internal/infrastructure/config"
	"github.com/afbora/event-driven-notification/internal/infrastructure/id"
	"github.com/afbora/event-driven-notification/internal/infrastructure/logger"
	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
	"github.com/afbora/event-driven-notification/internal/infrastructure/tracing"
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
	logger.Install(logger.Config{Level: cfg.LogLevel, Service: "reconciler"})

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	traceShutdown, err := tracing.Setup(rootCtx, tracing.Config{
		ServiceName: "reconciler",
		Endpoint:    cfg.OTLPEndpoint,
	})
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() { _ = traceShutdown(context.Background()) }()

	pool, err := pgxpool.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer pool.Close()

	notifRepo := pgadapter.NewNotificationRepository(pool)
	logRepo := pgadapter.NewNotificationLogRepository(pool)

	queue := asynqadapter.NewQueue(hibikenasynq.RedisClientOpt{Addr: cfg.RedisAddr})
	defer func() { _ = queue.Close() }()

	promRegistry := prometheus.NewRegistry()
	promRegistry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	_ = metrics.New(promRegistry) // reserve future emit points; registers Go + process baselines now

	metricsMux := nethttp.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{}))
	metricsSrv := &nethttp.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("reconciler metrics endpoint listening", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			slog.Error("reconciler metrics server stopped", "error", err.Error())
		}
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metricsSrv.Shutdown(shutCtx)
	}()

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
		"stuck_queued_reenqueued", out.StuckQueuedReenqueued,
	)
}
