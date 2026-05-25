package config_test

import (
	"testing"

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
