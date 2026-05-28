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
	"math/rand/v2"
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
	// the circuit and short-circuits subsequent calls (CLAUDE.md §3.5/§5,
	// ADR-0016). Thresholds come from config (defaults 5 failures / 10s
	// window / 30s open) rather than gobreaker's implicit defaults, so the
	// running behavior matches the documented contract and an operator can
	// tune it per deploy.
	guardedProvider := circuit.New(registry, breakerSettings(
		"provider-registry",
		cfg.CircuitMaxFailures,
		cfg.CircuitWindow,
		cfg.CircuitOpenTimeout,
		appMetrics,
	))

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

	// --- queue-depth sampler -------------------------------------------
	// The notifications_queue_depth gauge — and the HighQueueDepth alert
	// that reads it — is otherwise only written in tests, so the alert can
	// never fire in production. This loop polls asynq's pending backlog
	// every 15s and stamps the gauge with a live series. It owns its own
	// inspector connection and stops on rootCtx cancellation.
	depthQueue := asynqadapter.NewQueue(hibikenasynq.RedisClientOpt{Addr: cfg.RedisAddr})
	defer func() { _ = depthQueue.Close() }()
	go sampleQueueDepth(rootCtx, depthQueue, appMetrics, 15*time.Second)

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
// application package's RetryDelayFor and then layers jitter on top.
// Asynq calls this when a task's handler returns a non-nil error; the
// application package owns the deterministic policy so the routing keys
// (typed sentinels) and the timing (exponential / rate-limit) live
// together and stay unit-testable, while the jitter the constitution
// calls for (CLAUDE.md §5, "exponential backoff with jitter") is added
// here at the boundary. n is 1-indexed by asynq (the count of the
// attempt that just failed).
func retryDelay(n int, err error, _ *hibikenasynq.Task) time.Duration {
	return withJitter(application.RetryDelayFor(n, err), rand.Int64N)
}

// maxRetryJitter caps the random spread added to each retry delay. Bounding
// the jitter to a fixed ceiling (rather than scaling it to the full backoff)
// de-correlates a thundering herd without letting late exponential delays
// grow an unbounded random tail.
const maxRetryJitter = 30 * time.Second

// withJitter spreads retries to avoid a thundering herd: when a provider
// recovers, many tasks whose backoff expired at the same instant would
// otherwise re-fire together and re-overload it. The deterministic base
// (application.RetryDelayFor) is left untouched so unit tests can pin exact
// values; jitter is applied only here at the asynq boundary. It is additive
// — it never shortens the base, so the rate-limit floor still holds — and the
// added amount is clamped to maxRetryJitter. draw is injected (rand.Int64N in
// production) so the bounds are testable without real randomness.
func withJitter(base time.Duration, draw func(int64) int64) time.Duration {
	if base <= 0 {
		return base
	}
	bound := base
	if bound > maxRetryJitter {
		bound = maxRetryJitter
	}
	return base + time.Duration(draw(int64(bound)))
}

// sampleQueueDepth polls asynq's pending backlog on a fixed interval and
// stamps the notifications_queue_depth gauge so the HighQueueDepth alert has
// a live series to evaluate (the gauge is otherwise only written in tests).
// Extracted from run() so that stays a thin wiring routine; it returns when
// ctx is cancelled. A sampling error is logged and skipped — a transient
// inspector hiccup must not kill the loop and freeze the gauge.
func sampleQueueDepth(ctx context.Context, q *asynqadapter.Queue, m *metrics.Metrics, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			depths, err := q.QueueDepths(ctx)
			if err != nil {
				slog.Warn("queue depth sample failed", "error", err.Error())
				continue
			}
			for name, depth := range depths {
				m.SetQueueDepth(name, depth)
			}
		}
	}
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

// breakerSettings returns the gobreaker settings the worker uses for the
// provider registry. The thresholds are explicit (ADR-0016) so the running
// breaker matches CLAUDE.md §5's documented behavior instead of gobreaker's
// implicit defaults (which trip at >5 *consecutive* failures and stay open
// 60s — both wrong against the constitution):
//
//   - ReadyToTrip fires once TotalFailures reaches maxFailures within the
//     current counting window. The breaker only counts transient failures as
//     failures (see internal/infrastructure/circuit), so a burst of caller
//     4xx errors never trips it — only genuine provider sickness does.
//   - Interval (window) is gobreaker's closed-state count-clear period, so
//     "maxFailures within window" is a fixed window, not a sliding one.
//   - Timeout (openTimeout) is how long the breaker fails fast before letting
//     a single half-open probe (MaxRequests=1) through.
//
// OnStateChange is wired so transitions land on the
// notifications_circuit_breaker_state gauge (0=closed, 1=open, 2=half-open).
// m may be nil — the callback short-circuits and the gauge stays unobserved,
// matching the previous behavior on bare Settings construction.
func breakerSettings(name string, maxFailures int, window, openTimeout time.Duration, m *metrics.Metrics) gobreaker.Settings {
	return gobreaker.Settings{
		Name:        name,
		MaxRequests: 1,
		Interval:    window,
		Timeout:     openTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			// uint32 -> int is safe on the 64-bit targets we ship; comparing
			// in int avoids a lossy int -> uint32 conversion of the config value.
			return int(c.TotalFailures) >= maxFailures
		},
		OnStateChange: func(stateName string, _, to gobreaker.State) {
			if m == nil {
				return
			}
			m.SetCircuitBreakerState(stateName, breakerStateToMetric(to))
		},
	}
}

// breakerStateToMetric maps gobreaker's State enum to the typed
// metrics.CircuitBreakerState the gauge expects. The mapping is
// fixed by ADR conventions (0=closed, 1=open, 2=half-open) so
// Grafana panels can pivot on numeric thresholds.
func breakerStateToMetric(s gobreaker.State) metrics.CircuitBreakerState {
	switch s {
	case gobreaker.StateOpen:
		return metrics.CircuitOpen
	case gobreaker.StateHalfOpen:
		return metrics.CircuitHalfOpen
	default:
		return metrics.CircuitClosed
	}
}
