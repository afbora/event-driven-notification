// Package clock holds the production ports.Clock implementation. Tests
// inject a frozen clock from internal/application/fakes_test.go.
package clock

import "time"

// Real returns the wall clock's current time. Stateless, safe for
// concurrent use, allocation-free.
type Real struct{}

// New constructs a real clock. The function exists for symmetry with
// other constructors; *Real{} is equally valid.
func New() *Real { return &Real{} }

// Now returns time.Now() in UTC. UTC is the project-wide canonical
// timezone (CLAUDE.md §11 — all timestamps are stored as TIMESTAMPTZ
// and emitted in UTC).
func (*Real) Now() time.Time { return time.Now().UTC() }
