// Package logger owns the project's slog configuration (CLAUDE.md
// §12.2). It returns JSON-emitting *slog.Loggers that auto-attach
// the configured service name and the correlation id stashed on the
// request context. Every binary (cmd/api, cmd/worker, cmd/reconciler)
// installs one of these at startup so log lines aggregate cleanly
// across the three.
//
// Field schema, by contract:
//
//	time              RFC3339Nano timestamp (slog default)
//	level             DEBUG | INFO | WARN | ERROR
//	msg               human-readable summary
//	service           api | worker | reconciler
//	correlation_id    end-to-end request id (omitted if absent)
//
// Additional fields are attached per call site via slog.With or the
// XxxContext methods.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/afbora/event-driven-notification/internal/infrastructure/correlation"
)

// Config carries the knobs each binary's main wires up at startup.
// Out defaults to os.Stdout when nil; Level defaults to info when
// empty so a fresh deploy does not need to flip a debug switch.
type Config struct {
	Level   string    // debug / info / warn / error; empty → info
	Service string    // api / worker / reconciler — stamped on every record
	Out     io.Writer // log destination; nil → os.Stdout
}

// New builds a JSON slog.Logger that auto-stamps service and the
// correlation id from the call's context. Use *Context variants
// (InfoContext, WarnContext, ...) to make the context — and thus
// the correlation id — visible to the handler.
func New(cfg Config) *slog.Logger {
	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}
	base := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: parseLevel(cfg.Level)})
	return slog.New(&contextHandler{inner: base, service: cfg.Service})
}

// Install replaces slog.Default with a logger built from cfg, so
// package-level slog.* calls pick up the configuration. Every
// cmd/main.go calls this exactly once during startup.
func Install(cfg Config) {
	slog.SetDefault(New(cfg))
}

// contextHandler decorates a base slog.Handler to attach service +
// correlation_id on every record. The fields are pulled per-record
// rather than baked in via WithAttrs so a context's id can change
// between calls on the same logger (e.g. a long-lived worker
// reusing one *slog.Logger across many requests).
type contextHandler struct {
	inner   slog.Handler
	service string
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.service != "" {
		r.AddAttrs(slog.String("service", h.service))
	}
	if id := correlation.FromContext(ctx); id != "" {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs), service: h.service}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name), service: h.service}
}

// parseLevel maps the project's level strings onto slog levels.
// Unknown values resolve to info — a misconfigured deploy still
// emits useful logs rather than silently going dark.
func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
