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

	// --- reconciler -------------------------------------------------
	ReconcilerInterval time.Duration
}

// Load reads every env var defined above with sensible defaults and
// returns a validated Config. The error names the offending variable
// so a misconfigured deploy fails fast and obviously.
func Load() (Config, error) {
	cfg := Config{
		Env:         getString("APP_ENV", "dev"),
		LogLevel:    getString("LOG_LEVEL", "info"),
		DatabaseURL: getString("DATABASE_URL", "postgres://notification:notification@localhost:5432/notification?sslmode=disable"),
		RedisAddr:   getString("REDIS_ADDR", "localhost:6379"),

		HTTPAddr:             getString("HTTP_ADDR", ":8080"),
		WebhookProviderURL:   getString("WEBHOOK_PROVIDER_URL", ""),
		WebhookProviderToken: getString("WEBHOOK_PROVIDER_TOKEN", ""),
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

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.RedisAddr == "" {
		return Config{}, errors.New("REDIS_ADDR is required")
	}
	return cfg, nil
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
