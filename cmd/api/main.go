// Package main runs the api binary — the read/write HTTP frontend.
//
// Wiring order (top-to-bottom inside run):
//
//  1. Config (env vars → typed Config).
//  2. Infrastructure singletons: structured logger, postgres pool,
//     redis client, asynq queue, prometheus registry, clock, id gen.
//  3. Domain-facing adapters: repositories, idempotency store,
//     inbound rate limiter, status broadcaster, provider registry.
//  4. Application use cases.
//  5. HTTP layer: chi router with the canonical middleware chain,
//     the strict-server, the WebSocket handler, docs, health.
//  6. Boot the HTTP server with graceful shutdown wired to SIGTERM/INT.
//
// Every binary in this project follows the same shape so reviewers can
// open any cmd/* and find their bearings immediately.
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
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	wsadapter "github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/infrastructure/clock"
	"github.com/afbora/event-driven-notification/internal/infrastructure/config"
	"github.com/afbora/event-driven-notification/internal/infrastructure/id"
	"github.com/afbora/event-driven-notification/internal/infrastructure/logger"
	"github.com/afbora/event-driven-notification/internal/infrastructure/metrics"
	"github.com/afbora/event-driven-notification/internal/infrastructure/tracing"

	hibikenasynq "github.com/hibiken/asynq"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited with error", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger.Install(logger.Config{Level: cfg.LogLevel, Service: "api"})

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Tracing: no-op when OTEL_EXPORTER_OTLP_ENDPOINT is empty.
	traceShutdown, err := tracing.Setup(rootCtx, tracing.Config{
		ServiceName: "api",
		Endpoint:    cfg.OTLPEndpoint,
	})
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = traceShutdown(shutCtx)
	}()

	// --- pg + redis -----------------------------------------------------
	pool, err := pgxpool.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer pool.Close()

	redis := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})
	defer func() { _ = redis.Close() }()

	// --- asynq ----------------------------------------------------------
	asynqQueue := asynqadapter.NewQueue(hibikenasynq.RedisClientOpt{Addr: cfg.RedisAddr})
	defer func() { _ = asynqQueue.Close() }()

	// --- shared singletons ---------------------------------------------
	idGen := id.New()
	wallClock := clock.New()
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	appMetrics := metrics.New(registry)

	// --- repositories + adapters ---------------------------------------
	notifRepo := pgadapter.NewNotificationRepository(pool)
	batchRepo := pgadapter.NewBatchRepository(pool)
	tmplRepo := pgadapter.NewTemplateRepository(pool)
	logRepo := pgadapter.NewNotificationLogRepository(pool)

	idempStore := redisadapter.NewIdempotencyStore(redis)
	// Inbound rate limiter reuses the OutboundRateLimiter implementation
	// — the underlying fixed-window Lua script is the same; only the
	// bucket prefix differs (CLAUDE.md §2.6).
	inboundLimiter := redisadapter.NewOutboundRateLimiter(redis, cfg.InboundRateLimit, cfg.InboundRateWindow)

	// --- application use cases -----------------------------------------
	createNotif := application.NewCreateNotification(notifRepo, logRepo, tmplRepo, asynqQueue, idGen, wallClock, appMetrics)
	createBatch := application.NewCreateBatch(batchRepo, notifRepo, logRepo, asynqQueue, idGen, wallClock, appMetrics)
	getNotif := application.NewGetNotification(notifRepo)
	listNotifs := application.NewListNotifications(notifRepo)
	cancelNotif := application.NewCancelNotification(notifRepo, logRepo, asynqQueue, idGen, wallClock)
	traceNotif := application.NewGetNotificationTrace(notifRepo, logRepo)
	getBatch := application.NewGetBatch(batchRepo)
	createTmpl := application.NewCreateTemplate(tmplRepo, idGen, wallClock)
	getTmpl := application.NewGetTemplate(tmplRepo)
	listTmpls := application.NewListTemplates(tmplRepo)
	replaceTmpl := application.NewReplaceTemplate(tmplRepo, wallClock)
	deleteTmpl := application.NewDeleteTemplate(tmplRepo)

	// --- websocket -----------------------------------------------------
	hub := wsadapter.NewHubWithMetrics(appMetrics)
	wsConsumer := wsadapter.NewConsumer(redis, hub)
	consumerCtx, cancelConsumer := context.WithCancel(rootCtx)
	defer cancelConsumer()
	go func() {
		if err := wsConsumer.Run(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("websocket consumer stopped", "error", err.Error())
		}
	}()

	// --- HTTP layer ----------------------------------------------------
	server := httpadapter.NewServer(httpadapter.ServerOptions{
		CreateNotification:   createNotif.Execute,
		CreateBatch:          createBatch.Execute,
		GetNotification:      getNotif.Execute,
		ListNotifications:    listNotifs.Execute,
		CancelNotification:   cancelNotif.Execute,
		GetNotificationTrace: traceNotif.Execute,
		GetBatch:             getBatch.Execute,
		CreateTemplate:       createTmpl.Execute,
		GetTemplate:          getTmpl.Execute,
		ListTemplates:        listTmpls.Execute,
		ReplaceTemplate:      replaceTmpl.Execute,
		DeleteTemplate:       deleteTmpl.Execute,
		ReadinessChecks: []httpadapter.ReadinessCheck{
			func(ctx context.Context) error { return pool.Ping(ctx) },
			func(ctx context.Context) error { return redis.Ping(ctx).Err() },
		},
		PrometheusGatherer: registry,
		JSONMetrics: func(_ context.Context) (httpadapter.JSONMetricsSnapshot, error) {
			// Phase 5/6 wires a real Prometheus-querying snapshot; for
			// now the endpoint returns zeroes so it does not 501 in
			// smoke tests.
			return httpadapter.JSONMetricsSnapshot{}, nil
		},
	})

	router := httpadapter.NewRouter(httpadapter.Config{
		Middlewares: []func(nethttp.Handler) nethttp.Handler{
			httpadapter.CorrelationIDMiddleware(idGen),
			httpadapter.MetricsMiddleware(appMetrics),
			httpadapter.InboundRateLimitMiddleware(inboundLimiter),
			httpadapter.IdempotencyMiddleware(idempStore),
		},
	})

	// Strict server endpoints (everything from openapi.yaml).
	api.HandlerFromMux(api.NewStrictHandlerWithOptions(
		server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpadapter.RespondWithError,
			ResponseErrorHandlerFunc: httpadapter.RespondWithError,
		},
	), router)

	// Side-channel routes: WebSocket upgrade + docs (not in openapi).
	router.Handle("/api/v1/ws/notifications", httpadapter.NewWebSocketHandler(hub))
	httpadapter.MountDocs(router)

	// otelhttp wraps the router so every inbound request gets a
	// span — even when the global provider is no-op the spans are
	// cheap, so this stays on by default.
	tracedHandler := otelhttp.NewHandler(router, "api")
	httpServer := &nethttp.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           tracedHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// --- run + graceful shutdown ---------------------------------------
	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server failed: %w", err)
	case <-rootCtx.Done():
		slog.Info("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("api stopped cleanly")
	return nil
}
