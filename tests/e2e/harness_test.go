//go:build e2e

// Package e2e_test owns the end-to-end suite. Every test in this
// package brings up a real Postgres + Redis (via testcontainers), wires
// the full production stack (HTTP server, worker, websocket hub, all
// adapters and use cases), and exercises behavior through the public
// HTTP surface — no internal short-circuits.
//
// Run with:
//
//	go test -tags=e2e ./tests/e2e/...
//
// Cost: each Harness spins up two containers + an asynq server, so
// the suite is slower than unit tests by an order of magnitude. Tests
// share nothing — every t gets a fresh Harness so a flake in one case
// cannot poison another.
package e2e_test

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // pg driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // file source

	hibikenasynq "github.com/hibiken/asynq"

	asynqadapter "github.com/afbora/event-driven-notification/internal/adapters/asynq"
	httpadapter "github.com/afbora/event-driven-notification/internal/adapters/http"
	"github.com/afbora/event-driven-notification/internal/adapters/http/api"
	pgadapter "github.com/afbora/event-driven-notification/internal/adapters/postgres"
	provideradapter "github.com/afbora/event-driven-notification/internal/adapters/provider"
	redisadapter "github.com/afbora/event-driven-notification/internal/adapters/redis"
	wsadapter "github.com/afbora/event-driven-notification/internal/adapters/websocket"
	"github.com/afbora/event-driven-notification/internal/application"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/infrastructure/clock"
	"github.com/afbora/event-driven-notification/internal/infrastructure/id"
)

// Harness owns every long-lived resource an e2e test needs: the two
// real-service containers, the in-process HTTP server, the asynq
// server consuming tasks, and the WebSocket hub fanning out status
// updates. Tests interact with Harness exclusively through the
// public HTTP surface (BaseURL) plus a few diagnostic handles
// (Provider for fault injection, Pool/Redis for assertions that the
// HTTP surface alone cannot reach).
type Harness struct {
	BaseURL string

	// Diagnostic handles exposed for tests that need to peek at
	// internal state or steer provider behavior. Use sparingly — the
	// preferred axis is the HTTP API itself.
	Pool      *pgxpool.Pool
	Redis     *goredis.Client
	RedisAddr string // host:port for tests that need to build an asynq Inspector
	Provider  *provideradapter.MockProvider
	Hub       *wsadapter.Hub
}

// HarnessOption tunes a Harness at construction time. Used by tests
// that need to override a default — most often the inbound rate
// limit, which the suite-wide default sets very high so polling tests
// do not trip it.
type HarnessOption func(*harnessConfig)

type harnessConfig struct {
	inboundRateLimit     int
	outboundRateLimit    int
	outboundRateLimitFor time.Duration
}

// WithInboundRateLimit overrides the harness's default inbound rate
// limit (100000 req/min) with the supplied limit per minute. The
// dedicated rate-limit e2e test (PLAN phase 5 task 6) uses this to
// verify the production 60 req/min cap end-to-end.
func WithInboundRateLimit(limit int) HarnessOption {
	return func(c *harnessConfig) { c.inboundRateLimit = limit }
}

// WithOutboundRateLimit overrides the worker's per-channel outbound
// cap. Tests pass a long window (60s+) so the assertion stays
// deterministic regardless of how long the test itself runs.
func WithOutboundRateLimit(limit int, window time.Duration) HarnessOption {
	return func(c *harnessConfig) {
		c.outboundRateLimit = limit
		c.outboundRateLimitFor = window
	}
}

// NewHarness brings up the full stack. The supplied ctx is used for
// container startup; the harness itself does not adopt it for the
// lifetime of the HTTP server. Tests pass a generous timeout (60-90s)
// to cover image pulls on first run.
//
// Cleanup is registered via t.Cleanup so callers do not have to
// remember to defer anything — the testcontainer teardown happens
// even when the test fails.
func NewHarness(ctx context.Context, t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()

	cfg := harnessConfig{
		inboundRateLimit:     100000,
		outboundRateLimit:    100,
		outboundRateLimitFor: time.Second,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	pool, pgCleanup := startPostgres(ctx, t)
	redisClient, redisAddr, redisCleanup := startRedis(ctx, t)

	hub := wsadapter.NewHub()
	mockProvider := provideradapter.NewMockProvider()

	// --- adapters + use cases ------------------------------------------
	notifRepo := pgadapter.NewNotificationRepository(pool)
	batchRepo := pgadapter.NewBatchRepository(pool)
	tmplRepo := pgadapter.NewTemplateRepository(pool)
	logRepo := pgadapter.NewNotificationLogRepository(pool)

	idempStore := redisadapter.NewIdempotencyStore(redisClient)
	// Inbound limiter is deliberately high by default so polling
	// tests (200ms cadence over 30s = 150 requests) do not trip it.
	// The dedicated rate-limit e2e test (PLAN phase 5 task 6) lowers
	// the cap via WithInboundRateLimit to assert the 429 behavior
	// end-to-end.
	inboundLimiter := redisadapter.NewOutboundRateLimiter(redisClient, cfg.inboundRateLimit, time.Minute)
	outboundLimiter := redisadapter.NewOutboundRateLimiter(redisClient, cfg.outboundRateLimit, cfg.outboundRateLimitFor)
	broadcaster := redisadapter.NewStatusBroadcaster(redisClient)

	queue := asynqadapter.NewQueue(hibikenasynq.RedisClientOpt{Addr: redisAddr})

	idGen := id.New()
	wallClock := clock.New()

	createNotif := application.NewCreateNotification(notifRepo, logRepo, tmplRepo, queue, idGen, wallClock, nil)
	createBatch := application.NewCreateBatch(batchRepo, notifRepo, logRepo, queue, idGen, wallClock, nil)
	getNotif := application.NewGetNotification(notifRepo)
	listNotifs := application.NewListNotifications(notifRepo)
	cancelNotif := application.NewCancelNotification(notifRepo, logRepo, queue, idGen, wallClock)
	traceNotif := application.NewGetNotificationTrace(notifRepo, logRepo)
	getBatch := application.NewGetBatch(batchRepo)
	createTmpl := application.NewCreateTemplate(tmplRepo, idGen, wallClock)
	getTmpl := application.NewGetTemplate(tmplRepo)
	listTmpls := application.NewListTemplates(tmplRepo)
	replaceTmpl := application.NewReplaceTemplate(tmplRepo, wallClock)
	deleteTmpl := application.NewDeleteTemplate(tmplRepo)

	processUC := application.NewProcessNotification(application.ProcessNotificationDeps{
		Repo:        notifRepo,
		LogRepo:     logRepo,
		Provider:    mockProvider,
		RateLimiter: outboundLimiter,
		Broadcaster: broadcaster,
		IDGen:       idGen,
		Clock:       wallClock,
	})

	// --- worker -------------------------------------------------------
	asynqSrv := hibikenasynq.NewServer(
		hibikenasynq.RedisClientOpt{Addr: redisAddr},
		hibikenasynq.Config{
			Concurrency: 4,
			Queues: map[string]int{
				string(domain.PriorityHigh):   6,
				string(domain.PriorityNormal): 3,
				string(domain.PriorityLow):    1,
			},
			LogLevel: hibikenasynq.WarnLevel, // keep test output readable
		},
	)
	processor := asynqadapter.NewProcessor(processUC.Execute)
	mux := hibikenasynq.NewServeMux()
	processor.Register(mux)

	// Start (not Run) so the Server does NOT install its own signal
	// handler. On Windows the test process already owns SIGINT/SIGTERM
	// via testcontainers' reaper, and the two installations race —
	// see https://github.com/hibiken/asynq/blob/v0.26.0/signals_windows.go
	require.NoError(t, asynqSrv.Start(mux), "start asynq worker")

	// --- websocket consumer -------------------------------------------
	wsConsumer := wsadapter.NewConsumer(redisClient, hub)
	wsCtx, wsCancel := context.WithCancel(context.Background())
	wsDone := make(chan struct{})
	go func() {
		defer close(wsDone)
		if err := wsConsumer.Run(wsCtx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("websocket consumer stopped: %v", err)
		}
	}()

	// --- HTTP server --------------------------------------------------
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
			func(c context.Context) error { return pool.Ping(c) },
			func(c context.Context) error { return redisClient.Ping(c).Err() },
		},
	})

	router := httpadapter.NewRouter(httpadapter.Config{
		Middlewares: []func(nethttp.Handler) nethttp.Handler{
			httpadapter.CorrelationIDMiddleware(idGen),
			httpadapter.InboundRateLimitMiddleware(inboundLimiter, nil),
			httpadapter.IdempotencyMiddleware(idempStore),
		},
	})
	api.HandlerFromMux(api.NewStrictHandlerWithOptions(
		server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  httpadapter.RespondWithError,
			ResponseErrorHandlerFunc: httpadapter.RespondWithError,
		},
	), router)
	router.Handle("/api/v1/ws/notifications", httpadapter.NewWebSocketHandler(hub))

	httpSrv := httptest.NewServer(asChiRouter(router))

	// --- teardown -----------------------------------------------------
	t.Cleanup(func() {
		httpSrv.Close()
		asynqSrv.Shutdown()
		_ = queue.Close()
		wsCancel()
		<-wsDone
		_ = redisClient.Close()
		redisCleanup()
		pgCleanup()
	})

	return &Harness{
		BaseURL:   httpSrv.URL,
		Pool:      pool,
		Redis:     redisClient,
		RedisAddr: redisAddr,
		Provider:  mockProvider,
		Hub:       hub,
	}
}

// asChiRouter is a tiny helper that makes the conversion from chi.Router
// to http.Handler explicit at the call site (chi.Router does satisfy
// http.Handler, but the indirection is easier to read).
func asChiRouter(r chi.Router) nethttp.Handler { return r }

// startPostgres brings up a Postgres 16 container and applies every
// migration in db/migrations against it. Returns the pool and a
// cleanup func.
func startPostgres(ctx context.Context, t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("notification"),
		tcpostgres.WithUsername("notification"),
		tcpostgres.WithPassword("notification"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "postgres dsn")

	// Apply migrations from the project root. We resolve the
	// migrations directory relative to THIS source file so the test
	// is robust to the test runner's cwd.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsPath := "file://" + filepath.ToSlash(filepath.Join(filepath.Dir(thisFile), "..", "..", "db", "migrations"))

	m, err := migrate.New(migrationsPath, dsn)
	require.NoError(t, err, "open migrate")
	defer func() { _, _ = m.Close() }()
	require.NoError(t, m.Up(), "apply migrations")

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "pgx pool")

	cleanup := func() {
		pool.Close()
		termCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := container.Terminate(termCtx); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	}
	return pool, cleanup
}

// startRedis brings up a Redis 7 container and returns a connected
// go-redis client, the host:port string (for asynq config), and a
// cleanup func.
func startRedis(ctx context.Context, t *testing.T) (*goredis.Client, string, func()) {
	t.Helper()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "redis endpoint")

	client := goredis.NewClient(&goredis.Options{Addr: endpoint})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(pingCtx).Err(), "redis ping")

	cleanup := func() {
		termCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := container.Terminate(termCtx); err != nil {
			t.Logf("terminate redis: %v", err)
		}
	}
	return client, endpoint, cleanup
}
