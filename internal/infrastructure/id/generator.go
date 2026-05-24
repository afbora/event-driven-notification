// Package id holds the concrete ports.IDGenerator implementation used
// by every binary. Tests use the deterministic fake in
// internal/application/fakes_test.go; production wires *Generator.
package id

import (
	"github.com/google/uuid"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// Generator produces UUID v7 identifiers for every domain id and a
// fresh UUID v4 string for correlation ids. UUID v7 is lexicographically
// sortable by creation time (CLAUDE.md §11 / ADR-0001 — primary keys
// stay friendly to PG B-tree indexes), v4 is fine for correlation ids
// because they are never used as a primary key.
type Generator struct{}

// New returns a ready-to-use Generator. No configuration needed — the
// type is stateless and safe for concurrent use.
func New() *Generator { return &Generator{} }

// NewNotificationID returns a fresh UUID v7. Falls back to UUID v4 if
// v7 generation ever fails — UUID v7 calls into the OS rand source
// and an error here would mean the OS itself is in trouble; in that
// case a v4 keeps the system usable until ops investigates.
func (g *Generator) NewNotificationID() domain.NotificationID {
	return domain.NotificationID(newUUIDv7OrV4())
}

// NewBatchID is UUID v7 — same rationale as notification ids.
func (g *Generator) NewBatchID() domain.BatchID {
	return domain.BatchID(newUUIDv7OrV4())
}

// NewTemplateID is UUID v7 — same rationale.
func (g *Generator) NewTemplateID() domain.TemplateID {
	return domain.TemplateID(newUUIDv7OrV4())
}

// NewLogID is UUID v7 — same rationale.
func (g *Generator) NewLogID() domain.LogID {
	return domain.LogID(newUUIDv7OrV4())
}

// NewCorrelationID returns a UUID v4 as a plain string. The format is
// not load-bearing — anything that round-trips through an HTTP header
// works. v4 is a fine compromise that does not leak creation time.
func (g *Generator) NewCorrelationID() string {
	return uuid.NewString()
}

// newUUIDv7OrV4 returns a UUID v7 when the OS rand source cooperates;
// otherwise a v4 so the caller never sees an error path on a hot
// happy-path code section.
func newUUIDv7OrV4() string {
	if v7, err := uuid.NewV7(); err == nil {
		return v7.String()
	}
	return uuid.NewString()
}
