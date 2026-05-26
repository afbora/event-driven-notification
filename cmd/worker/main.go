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
	"github.com/afbora/event-driven-notification/internal/infrastructure/logger"
	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
	"github.com/afbora/event-driven-notification/internal/infrastructure/tracing"
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
	logger.Install(logger.Config{Level: cfg.LogLevel, Service: "worker"})

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	traceShutdown, err := tracing.Setup(rootCtx, tracing.Config{
		ServiceName: "worker",
		Endpoint:    cfg.OTLPEndpoint,
	})
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() { _ = traceShutdown(context.Background()) }()

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
	promRegistry := prometheus.NewRegistry()
	promRegistry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	appMetrics := metrics.New(promRegistry)

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
	// Tracer is wired via the dedicated port adapter so the application
	// layer never imports go.opentelemetry.io/otel directly
	// (CLAUDE.md §3.3). Spans remain no-ops until tracing.Setup
	// installs a real exporter.
	appTracer := tracing.NewTracer("github.com/afbora/event-driven-notification/internal/application")
	processUC := application.NewProcessNotification(application.ProcessNotificationDeps{
		Repo:        notifRepo,
		LogRepo:     logRepo,
		Provider:    guardedProvider,
		RateLimiter: outboundLimiter,
		Broadcaster: broadcaster,
		IDGen:       idGen,
		Clock:       wallClock,
		Metrics:     appMetrics,
		Tracer:      appTracer,
	})

	// --- asynq server --------------------------------------------------
	// RetryDelayFunc routes asynq's native retry by the typed sentinel
	// the use case returns (ADR-0015). ErrOutboundRateLimited picks the
	// short rate-limit backoff so the throttled task re-fires quickly
	// once the window rolls forward; ErrProviderTransient (and any
	// other non-sentinel error from the use case, e.g. an infra
	// error during the claim path) picks the exponential schedule
	// matching the application's backoffFor function.
	srv := hibikenasynq.NewServer(
		hibikenasynq.RedisClientOpt{Addr: cfg.RedisAddr},
		hibikenasynq.Config{
			Concurrency: cfg.WorkerConcurrency,
			Queues: map[string]int{
				string(domain.PriorityHigh):   6,
				string(domain.PriorityNormal): 3,
				string(domain.PriorityLow):    1,
			},
			RetryDelayFunc: retryDelay,
		},
	)
	processor := asynqadapter.NewProcessor(processUC.Execute)

	mux := hibikenasynq.NewServeMux()
	processor.Register(mux)

	// --- metrics endpoint ----------------------------------------------
	// A tiny HTTP server exposes /metrics so Prometheus can scrape
	// the worker's registry. Lives on cfg.MetricsAddr (default
	// :9090) — separate from any application HTTP listener because
	// the worker has none.
	metricsMux := nethttp.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{}))
	metricsSrv := &nethttp.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("worker metrics endpoint listening", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			slog.Error("worker metrics server stopped", "error", err.Error())
		}
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metricsSrv.Shutdown(shutCtx)
	}()

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

// retryDelay is the asynq.RetryDelayFunc shim that delegates to the
// application package's RetryDelayFor. Asynq calls this when a task's
// handler returns a non-nil error; the application package owns the
// actual policy so the routing keys (typed sentinels) and the timing
// (exponential / rate-limit) live together. n is 1-indexed by asynq
// (the count of the attempt that just failed).
func retryDelay(n int, err error, _ *hibikenasynq.Task) time.Duration {
	return application.RetryDelayFor(n, err)
}

// buildProvider chooses between webhook and mock per channel. Each
// channel could be configured independently later; for now the same
// shape applies to all three.
//
// MockProvider honors MOCK_PROVIDER_SUCCESS_RATE and
// MOCK_PROVIDER_FAILURE_MODE so the dev stack can be flipped into a
// failure mode at compose-time (see docker-compose.failtest.yml).
// Defaults remain production-equivalent (always succeed, transient
// shape when forced to fail) so `docker compose up -d` with no
// overrides ships the same behavior it did before this knob existed.
func buildProvider(cfg config.Config, _ domain.Channel) interface {
	Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult
} {
	if cfg.WebhookProviderURL != "" {
		return provideradapter.NewWebhookProvider(cfg.WebhookProviderURL, 5_000_000_000) // 5s
	}
	return provideradapter.NewMockProvider(
		provideradapter.WithSuccessRate(cfg.MockProviderSuccessRate),
		provideradapter.WithFailureMode(mockFailureMode(cfg.MockProviderFailureMode)),
	)
}

// mockFailureMode translates the config string into the typed
// MockProvider option. config.Load has already validated the value, so
// any unexpected string here would be a programmer bug; we fall back
// to the safe default (transient) rather than panicking — a misrouted
// failure-mode string is observability noise, not corruption.
func mockFailureMode(mode string) provideradapter.FailureMode {
	if mode == "permanent" {
		return provideradapter.FailurePermanent
	}
	return provideradapter.FailureTransient
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
