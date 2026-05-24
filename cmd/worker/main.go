// Package main runs the worker binary — the background processor
// that consumes notifications from asynq, invokes the provider, and
// writes the outcome back to Postgres + Redis pub/sub.
//
// Same wiring shape as cmd/api: load config, build singletons, build
// adapters, build the ProcessNotification use case, hand it to the
// asynq Server, and run until SIGTERM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	hibikenasynq "github.com/hibiken/asynq"
	"github.com/sony/gobreaker"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	provideradapter "github.com/afbora/event-driven-notification/internal/adapters/provider"
	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/infrastructure/circuit"
	"github.com/afbora/event-driven-notification/internal/infrastructure/clock"
	"github.com/afbora/event-driven-notification/internal/infrastructure/config"
	"github.com/afbora/event-driven-notification/internal/infrastructure/id"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited with error", "error", err.Error())
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

	// --- pg + redis -----------------------------------------------------
	pool, err := pgxpool.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer pool.Close()

	redis := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})
	defer func() { _ = redis.Close() }()

	// --- shared singletons ---------------------------------------------
	idGen := id.New()
	wallClock := clock.New()

	// --- repositories + adapters ---------------------------------------
	notifRepo := pgadapter.NewNotificationRepository(pool)
	logRepo := pgadapter.NewNotificationLogRepository(pool)
	outboundLimiter := redisadapter.NewOutboundRateLimiter(redis, cfg.OutboundRateLimit, cfg.OutboundRateWindow)
	broadcaster := redisadapter.NewStatusBroadcaster(redis)

	// --- provider registry ---------------------------------------------
	// Production wires a webhook provider per channel when
	// WEBHOOK_PROVIDER_URL is set; otherwise the mock is the safe
	// default (the dev compose stack ships without external providers).
	registry := provideradapter.NewRegistry()
	for _, ch := range []domain.Channel{domain.ChannelSMS, domain.ChannelEmail, domain.ChannelPush} {
		registry.Register(ch, buildProvider(cfg, ch))
	}
	// Wrap the registry in a circuit breaker so a sick provider opens
	// the circuit and short-circuits subsequent calls (CLAUDE.md §3.x,
	// ADR-0008). gobreaker's defaults are fine for this assessment;
	// thresholds become an ADR later if traffic grows.
	guardedProvider := circuit.New(registry, breakerSettings("provider-registry"))

	// --- application use case ------------------------------------------
	processUC := application.NewProcessNotification(
		notifRepo, logRepo,
		guardedProvider, outboundLimiter, broadcaster,
		idGen, wallClock,
	)

	// --- asynq server --------------------------------------------------
	srv := hibikenasynq.NewServer(
		hibikenasynq.RedisClientOpt{Addr: cfg.RedisAddr},
		hibikenasynq.Config{
			Concurrency: cfg.WorkerConcurrency,
			Queues: map[string]int{
				string(domain.PriorityHigh):   6,
				string(domain.PriorityNormal): 3,
				string(domain.PriorityLow):    1,
			},
		},
	)
	processor := asynqadapter.NewProcessor(processUC.Execute)

	mux := hibikenasynq.NewServeMux()
	processor.Register(mux)

	// asynq runs its own signal handler; we use rootCtx as a
	// best-effort kill switch and rely on srv.Shutdown for graceful
	// drain when the user hits Ctrl-C.
	errCh := make(chan error, 1)
	go func() {
		slog.Info("worker started",
			"concurrency", cfg.WorkerConcurrency,
			"redis", cfg.RedisAddr)
		if err := srv.Run(mux); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("worker server failed: %w", err)
	case <-rootCtx.Done():
		slog.Info("shutdown signal received")
	}

	srv.Shutdown()
	slog.Info("worker stopped cleanly")
	return nil
}

// buildProvider chooses between webhook and mock per channel. Each
// channel could be configured independently later; for now the same
// shape applies to all three.
func buildProvider(cfg config.Config, _ domain.Channel) interface {
	Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult
} {
	if cfg.WebhookProviderURL != "" {
		return provideradapter.NewWebhookProvider(cfg.WebhookProviderURL, 5_000_000_000) // 5s
	}
	return provideradapter.NewMockProvider()
}

// breakerSettings returns the gobreaker settings the worker uses for
// every provider. Modest thresholds because a production tuning
// requires real-world data — phase 5/6 may pull these into config.
func breakerSettings(name string) gobreaker.Settings {
	return gobreaker.Settings{
		Name:        name,
		MaxRequests: 1,
	}
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
