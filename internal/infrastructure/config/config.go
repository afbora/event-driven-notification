// Package config loads runtime configuration from environment
// variables (CLAUDE.md §3.7 / §2.7 — every value lives in
// docker-compose.yml, no .env file). Load is called once at startup
// from each cmd/*; on invalid input the process refuses to start with
// a clear error.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the union of every knob the API, worker, and reconciler
// binaries read. Each binary uses the subset it needs — wiring code
// pulls fields out one by one rather than passing the whole struct
// down into adapters.
type Config struct {
	// --- shared -----------------------------------------------------
	Env         string // dev / staging / prod (informational, used in logs)
	LogLevel    string // debug / info / warn / error
	DatabaseURL string
	RedisAddr   string

	// --- API --------------------------------------------------------
	HTTPAddr             string
	InboundRateLimit     int           // requests per InboundRateWindow
	InboundRateWindow    time.Duration // window for inbound limiter
	IdempotencyTTL       time.Duration
	WebhookProviderURL   string // optional webhook provider
	WebhookProviderToken string // bearer token for webhook provider

	// --- worker -----------------------------------------------------
	WorkerConcurrency  int
	OutboundRateLimit  int // messages per OutboundRateWindow per channel
	OutboundRateWindow time.Duration

	// MockProviderSuccessRate controls the local MockProvider's
	// deterministic success/failure split. Defaults to 1.0 (always
	// succeed) so the dev compose stack ships production-equivalent
	// behavior; the loadtest / failtest overlays set it to 0 to drive
	// the F (retry) and G (circuit breaker) end-to-end paths.
	MockProviderSuccessRate float64

	// MockProviderFailureMode picks the failure shape when the
	// success rate produces a failure: "transient" (5xx-class,
	// retryable) or "permanent" (4xx-class, non-retryable).
	// Defaults to "transient".
	MockProviderFailureMode string

	// --- circuit breaker (worker) -----------------------------------
	// Thresholds for the provider circuit breaker (ADR-0016). These map
	// directly onto CLAUDE.md §5's documented behavior: open after
	// CircuitMaxFailures transient failures within CircuitWindow, fail
	// fast for CircuitOpenTimeout, then let a single half-open probe
	// through. Defaults (5 / 10s / 30s) match the constitution; making
	// them configurable lets a deploy tune the breaker without a rebuild.
	CircuitMaxFailures int
	CircuitWindow      time.Duration
	CircuitOpenTimeout time.Duration

	// --- reconciler -------------------------------------------------
	ReconcilerInterval time.Duration

	// --- tracing ----------------------------------------------------
	OTLPEndpoint string // empty → no-op tracer

	// --- metrics endpoint (worker / reconciler) ---------------------
	// MetricsAddr is the listen address for a tiny http.Server that
	// exposes /metrics. The api already serves /metrics on HTTPAddr;
	// the worker and reconciler use this knob to surface their own
	// registries to Prometheus.
	MetricsAddr string
}

// Load reads every env var defined above with sensible defaults and
// returns a validated Config. The error names the offending variable
// so a misconfigured deploy fails fast and obviously.
func Load() (Config, error) {
	cfg := Config{
		Env:         getString("APP_ENV", "dev"),
		LogLevel:    getString("LOG_LEVEL", "info"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisAddr:   getString("REDIS_ADDR", "localhost:6379"),

		HTTPAddr:             getString("HTTP_ADDR", ":8080"),
		WebhookProviderURL:   getString("WEBHOOK_PROVIDER_URL", ""),
		WebhookProviderToken: getString("WEBHOOK_PROVIDER_TOKEN", ""),
		OTLPEndpoint:         getString("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		MetricsAddr:          getString("METRICS_ADDR", ":9090"),
	}

	var err error
	if cfg.InboundRateLimit, err = getInt("INBOUND_RATE_LIMIT", 60); err != nil {
		return Config{}, err
	}
	if cfg.InboundRateWindow, err = getDuration("INBOUND_RATE_WINDOW", time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.IdempotencyTTL, err = getDuration("IDEMPOTENCY_TTL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	// Code default is 25 (a sensible single-node ceiling). docker-compose.yml
	// deliberately overrides it to 10 for the dev stack so a developer laptop
	// is not saturated; production sets it per-deployment at the orchestration
	// layer. The two values diverging on purpose is documented in both places.
	if cfg.WorkerConcurrency, err = getInt("WORKER_CONCURRENCY", 25); err != nil {
		return Config{}, err
	}
	if cfg.OutboundRateLimit, err = getInt("OUTBOUND_RATE_LIMIT", 100); err != nil {
		return Config{}, err
	}
	if cfg.OutboundRateWindow, err = getDuration("OUTBOUND_RATE_WINDOW", time.Second); err != nil {
		return Config{}, err
	}
	if cfg.ReconcilerInterval, err = getDuration("RECONCILER_INTERVAL", time.Minute); err != nil {
		return Config{}, err
	}
	if err = loadMockProvider(&cfg); err != nil {
		return Config{}, err
	}
	if err = loadCircuitBreaker(&cfg); err != nil {
		return Config{}, err
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.RedisAddr == "" {
		return Config{}, errors.New("REDIS_ADDR is required")
	}
	return cfg, nil
}

// loadMockProvider parses and validates the local MockProvider knobs onto cfg.
// Split out of Load (alongside loadCircuitBreaker) so Load stays under the
// cognitive- and cyclomatic-complexity ceilings; the success-rate and
// failure-mode knobs are a self-contained group whose validation belongs
// together. Defaults are production-equivalent (always succeed, transient
// shape) per CLAUDE.md §2.7.
func loadMockProvider(cfg *Config) error {
	var err error
	if cfg.MockProviderSuccessRate, err = getFloat("MOCK_PROVIDER_SUCCESS_RATE", 1.0); err != nil {
		return err
	}
	if cfg.MockProviderSuccessRate < 0 || cfg.MockProviderSuccessRate > 1 {
		return fmt.Errorf("MOCK_PROVIDER_SUCCESS_RATE must be in [0, 1]; got %v", cfg.MockProviderSuccessRate)
	}
	cfg.MockProviderFailureMode = getString("MOCK_PROVIDER_FAILURE_MODE", "transient")
	if cfg.MockProviderFailureMode != "transient" && cfg.MockProviderFailureMode != "permanent" {
		return fmt.Errorf("MOCK_PROVIDER_FAILURE_MODE must be one of: transient, permanent; got %q", cfg.MockProviderFailureMode)
	}
	return nil
}

// loadCircuitBreaker parses and validates the circuit-breaker thresholds onto
// cfg (ADR-0016). Split out of Load so Load stays under the cognitive-
// complexity ceiling (gocognit / Sonar S3776) — the three knobs are a
// self-contained group. Each value must be positive: a zero or negative trip
// count or timeout would either trip instantly or never, so Load fails fast
// and names the offending var rather than shipping a dead breaker.
func loadCircuitBreaker(cfg *Config) error {
	var err error
	if cfg.CircuitMaxFailures, err = getInt("CIRCUIT_MAX_FAILURES", 5); err != nil {
		return err
	}
	if cfg.CircuitMaxFailures <= 0 {
		return fmt.Errorf("CIRCUIT_MAX_FAILURES must be a positive integer; got %d", cfg.CircuitMaxFailures)
	}
	if cfg.CircuitWindow, err = getDuration("CIRCUIT_WINDOW", 10*time.Second); err != nil {
		return err
	}
	if cfg.CircuitWindow <= 0 {
		return fmt.Errorf("CIRCUIT_WINDOW must be a positive duration; got %s", cfg.CircuitWindow)
	}
	if cfg.CircuitOpenTimeout, err = getDuration("CIRCUIT_OPEN_TIMEOUT", 30*time.Second); err != nil {
		return err
	}
	if cfg.CircuitOpenTimeout <= 0 {
		return fmt.Errorf("CIRCUIT_OPEN_TIMEOUT must be a positive duration; got %s", cfg.CircuitOpenTimeout)
	}
	return nil
}

func getString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func getInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid integer for %s: %w", key, err)
	}
	return v, nil
}

func getFloat(key string, fallback float64) (float64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float for %s: %w", key, err)
	}
	return v, nil
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %w", key, err)
	}
	return v, nil
}
