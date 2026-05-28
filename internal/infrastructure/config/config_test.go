package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/infrastructure/config"
)

// TestLoad_RequiresDatabaseURL: Load() must refuse to start when
// DATABASE_URL is unset. CLAUDE.md §2.7 makes docker-compose.yml the
// single source of env defaults; the Go source must not carry a
// fallback DSN with embedded credentials (sonarcloud S6698).
func TestLoad_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "DATABASE_URL")
}

// TestLoad_AcceptsValidDatabaseURL: with DATABASE_URL set, Load()
// returns the configured value verbatim. Ensures we did not introduce
// any silent transformation while removing the fallback.
func TestLoad_AcceptsValidDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@host:5432/db?sslmode=disable", cfg.DatabaseURL)
}

// TestLoad_MockProviderSuccessRate_DefaultIsProductionEquivalent: when
// the env var is unset, the success rate must default to 1.0 — the
// production-equivalent "always succeed" mode that the dev compose
// stack ships with today (CLAUDE.md §2.7). A reviewer running
// `docker compose up -d` must see the same behavior whether the new
// knob is documented in the env block or not.
func TestLoad_MockProviderSuccessRate_DefaultIsProductionEquivalent(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("MOCK_PROVIDER_SUCCESS_RATE", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.InDelta(t, 1.0, cfg.MockProviderSuccessRate, 1e-9,
		"unset MOCK_PROVIDER_SUCCESS_RATE must default to 1.0 (always succeed)")
}

// TestLoad_MockProviderSuccessRate_ParsesValidFloat: the loadtest /
// failtest compose overlays set this to 0 to drive the F (retry) and
// G (circuit breaker) end-to-end paths. Load must accept any value
// in [0, 1] and surface the parsed float on Config.
func TestLoad_MockProviderSuccessRate_ParsesValidFloat(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("MOCK_PROVIDER_SUCCESS_RATE", "0.25")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.InDelta(t, 0.25, cfg.MockProviderSuccessRate, 1e-9)
}

// TestLoad_MockProviderSuccessRate_RejectsOutOfRange: only [0, 1] is
// valid; the underlying MockProvider clamps but a deploy-time reject
// catches typos at startup rather than silently muting a misconfigured
// override.
func TestLoad_MockProviderSuccessRate_RejectsOutOfRange(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("MOCK_PROVIDER_SUCCESS_RATE", "1.5")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "MOCK_PROVIDER_SUCCESS_RATE",
		"the error must name the offending env var so deploys fail clearly")
}

// TestLoad_MockProviderFailureMode_DefaultIsTransient: when the env
// var is unset, the failure mode defaults to "transient" (5xx-class)
// so the F retry path is the natural failtest target. Permanent mode
// is opt-in via the compose overlay for the G circuit-breaker test.
func TestLoad_MockProviderFailureMode_DefaultIsTransient(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("MOCK_PROVIDER_FAILURE_MODE", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "transient", cfg.MockProviderFailureMode)
}

// TestLoad_MockProviderFailureMode_AcceptsBothKnownValues: "transient"
// and "permanent" are the only two values MockProvider supports; the
// config parses them verbatim and the worker translates each to the
// corresponding MockProvider option.
func TestLoad_MockProviderFailureMode_AcceptsBothKnownValues(t *testing.T) {
	for _, mode := range []string{"transient", "permanent"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
			t.Setenv("REDIS_ADDR", "redis:6379")
			t.Setenv("MOCK_PROVIDER_FAILURE_MODE", mode)

			cfg, err := config.Load()
			require.NoError(t, err)
			require.Equal(t, mode, cfg.MockProviderFailureMode)
		})
	}
}

// TestLoad_MockProviderFailureMode_RejectsUnknownValue: typos must
// fail at startup; silently falling back to a default would hide the
// real intent of an operator who set the var deliberately.
func TestLoad_MockProviderFailureMode_RejectsUnknownValue(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("MOCK_PROVIDER_FAILURE_MODE", "occasional")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "MOCK_PROVIDER_FAILURE_MODE")
}

// TestLoad_CircuitBreaker_Defaults: with the env vars unset, the breaker
// thresholds default to exactly what CLAUDE.md §5 documents — open after 5
// failures within a 10s window, fail fast for 30s. Pinning the defaults here
// keeps the code and the constitution in agreement (ADR-0016); without
// explicit settings gobreaker would silently fall back to trip-at->5-
// consecutive / 60s-open, contradicting the documented behavior.
func TestLoad_CircuitBreaker_Defaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CIRCUIT_MAX_FAILURES", "")
	t.Setenv("CIRCUIT_WINDOW", "")
	t.Setenv("CIRCUIT_OPEN_TIMEOUT", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 5, cfg.CircuitMaxFailures)
	require.Equal(t, 10*time.Second, cfg.CircuitWindow)
	require.Equal(t, 30*time.Second, cfg.CircuitOpenTimeout)
}

// TestLoad_CircuitBreaker_ParsesOverrides: operators tune the thresholds via
// env (ADR-0016's production-tuning path); Load surfaces the parsed values
// verbatim so a deploy can widen or tighten the breaker without a rebuild.
func TestLoad_CircuitBreaker_ParsesOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CIRCUIT_MAX_FAILURES", "8")
	t.Setenv("CIRCUIT_WINDOW", "20s")
	t.Setenv("CIRCUIT_OPEN_TIMEOUT", "45s")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 8, cfg.CircuitMaxFailures)
	require.Equal(t, 20*time.Second, cfg.CircuitWindow)
	require.Equal(t, 45*time.Second, cfg.CircuitOpenTimeout)
}

// TestLoad_CircuitBreaker_RejectsNonPositiveMaxFailures: a zero or negative
// trip threshold would either trip instantly or never — both are deploy
// errors, so Load refuses to start and names the offending var (CLAUDE.md
// §3.7 fail-fast on invalid config).
func TestLoad_CircuitBreaker_RejectsNonPositiveMaxFailures(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CIRCUIT_MAX_FAILURES", "0")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "CIRCUIT_MAX_FAILURES")
}

// TestLoad_CircuitBreaker_RejectsNonPositiveOpenTimeout: a non-positive open
// timeout would never let the breaker move to half-open (or move instantly),
// defeating the fail-fast window. Reject it at startup.
func TestLoad_CircuitBreaker_RejectsNonPositiveOpenTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db?sslmode=disable")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("CIRCUIT_OPEN_TIMEOUT", "0s")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "CIRCUIT_OPEN_TIMEOUT")
}
