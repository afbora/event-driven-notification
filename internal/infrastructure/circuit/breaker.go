// Package circuit wraps a ports.Provider in a sony/gobreaker circuit breaker
// so a flapping or downed provider cannot exhaust worker concurrency. The
// breaker counts only transient failures (5xx-class, timeouts, network
// errors) — permanent failures (4xx) are caller errors and do not indicate
// provider sickness.
//
// Default thresholds will be tuned via ADR once the production-like load
// numbers are in (CLAUDE.md mentions an ADR-0007 in the skill index); for
// now the caller supplies gobreaker.Settings directly.
package circuit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sony/gobreaker"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// Breaker decorates a ports.Provider with a circuit breaker. It itself
// satisfies ports.Provider, so it slots in transparently anywhere the
// worker expects a provider — typically wrapping the ProviderRegistry.
type Breaker struct {
	inner   ports.Provider
	breaker *gobreaker.CircuitBreaker
}

// New constructs a Breaker around an inner provider. settings.Name should
// be unique per provider so metrics (phase 6) can attribute open/closed
// transitions correctly.
func New(inner ports.Provider, settings gobreaker.Settings) *Breaker {
	return &Breaker{
		inner:   inner,
		breaker: gobreaker.NewCircuitBreaker(settings),
	}
}

// Send routes the call through the breaker. The inner provider's
// DeliveryResult shape decides whether the breaker counts the call as a
// failure: only transient failures (Retryable=true) count. Permanent
// failures pass through without tripping; successful calls reset the
// consecutive-failure counter.
func (b *Breaker) Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult {
	start := time.Now()
	res, err := b.breaker.Execute(func() (any, error) {
		r := b.inner.Send(ctx, channel, recipient, content)
		if !r.Success && r.Retryable {
			// Returning a non-nil error here is what flips the breaker
			// counter; the DeliveryResult itself comes back unchanged
			// in res for the caller.
			return r, fmt.Errorf("provider transient: %s", r.Reason)
		}
		return r, nil
	})
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			return domain.TransientFailureResult(
				fmt.Sprintf("circuit open for %q", channel),
				0,
				time.Since(start),
			)
		}
		// inner returned a transient failure that flipped the breaker — the
		// real DeliveryResult is in res (assertion below).
	}
	if dr, ok := res.(domain.DeliveryResult); ok {
		return dr
	}
	return domain.TransientFailureResult(
		"circuit breaker: unexpected nil result",
		0,
		time.Since(start),
	)
}
