package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/infrastructure/correlation"
	"github.com/afbora/event-driven-notification/internal/infrastructure/logger"
)

// decodeOne unmarshals the single JSON object captured in buf.
// Strict — fails the test on syntax errors or extra trailing content.
func decodeOne(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	dec := json.NewDecoder(buf)
	var out map[string]any
	require.NoError(t, dec.Decode(&out))
	return out
}

// TestNew_AlwaysIncludesService: every emitted record carries the
// configured service name so multi-binary log shipping (api / worker
// / reconciler) is trivially filterable.
func TestNew_AlwaysIncludesService(t *testing.T) {
	var buf bytes.Buffer
	lg := logger.New(logger.Config{Service: "api", Out: &buf})
	lg.Info("hello")

	out := decodeOne(t, &buf)
	require.Equal(t, "api", out["service"])
	require.Equal(t, "hello", out["msg"])
}

// TestNew_AttachesCorrelationIDFromContext: a record emitted with a
// context carrying a correlation id picks it up automatically. This
// is the load-bearing behavior — handlers, use cases, and worker
// goroutines all log via slog.InfoContext with the request ctx and
// the id flows through without any per-call boilerplate.
func TestNew_AttachesCorrelationIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	lg := logger.New(logger.Config{Service: "worker", Out: &buf})

	ctx := correlation.WithContext(context.Background(), "01HXYZTEST0001")
	lg.InfoContext(ctx, "processing")

	out := decodeOne(t, &buf)
	require.Equal(t, "01HXYZTEST0001", out["correlation_id"])
	require.Equal(t, "worker", out["service"])
}

// TestNew_OmitsCorrelationIDWhenAbsent: a plain log call (no
// context, or a context without the id) does not emit an empty
// correlation_id field. Operators filtering on "correlation_id != ”"
// must not see noise from bootstrap logs.
func TestNew_OmitsCorrelationIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	lg := logger.New(logger.Config{Service: "api", Out: &buf})
	lg.Info("bootstrap")

	out := decodeOne(t, &buf)
	_, present := out["correlation_id"]
	require.False(t, present, "correlation_id must be omitted when context has no id")
}

// TestNew_LevelFiltering: configured level filters lower levels.
// "warn" config drops debug and info, keeps warn and error.
func TestNew_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	lg := logger.New(logger.Config{Level: "warn", Service: "api", Out: &buf})

	lg.Debug("debug msg")
	lg.Info("info msg")
	require.Empty(t, buf.String(), "debug + info must be filtered out at warn level")

	lg.Warn("warn msg")
	require.NotEmpty(t, buf.String())
}

// TestNew_DefaultLevelIsInfo: an unconfigured level defaults to
// info — operators see useful traffic without needing to dial debug
// on every fresh deploy.
func TestNew_DefaultLevelIsInfo(t *testing.T) {
	var buf bytes.Buffer
	lg := logger.New(logger.Config{Service: "api", Out: &buf})

	lg.Debug("hidden")
	require.Empty(t, buf.String())

	lg.Info("visible")
	require.NotEmpty(t, buf.String())
}

// TestInstall_ReplacesSlogDefault: Install rewires slog.Default so
// package-level slog.* calls (the common pattern across this code
// base) pick up the new handler.
func TestInstall_ReplacesSlogDefault(t *testing.T) {
	// Capture original to restore — test isolation.
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	logger.Install(logger.Config{Service: "reconciler", Out: &buf})

	slog.Info("hi from default")
	out := decodeOne(t, &buf)
	require.Equal(t, "reconciler", out["service"])
}
