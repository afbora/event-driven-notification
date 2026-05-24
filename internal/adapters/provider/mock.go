// Package provider holds concrete implementations of ports.Provider — the
// strategy interface for delivering notifications through a specific channel
// (ADR-0004). Two implementations live here today:
//
//   - MockProvider: deterministic, configurable; used by tests and by the
//     local docker-compose stack when no real provider is wired up.
//   - WebhookProvider: HTTP POST against an arbitrary URL (webhook.site by
//     default per the brief).
//
// New providers (Twilio, SendGrid, FCM) add a struct next to these without
// touching the worker or use cases — see ADR-0004.
package provider

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/afbora/event-driven-notification/internal/domain"
)

// FailureMode picks which DeliveryResult shape MockProvider returns when
// the success rate decides on failure.
type FailureMode int

const (
	// FailureTransient produces a 5xx-class retryable result (default).
	FailureTransient FailureMode = iota
	// FailurePermanent produces a 4xx-class non-retryable result.
	FailurePermanent
)

// Call captures every Send invocation so tests can assert on the
// arguments the worker actually passed.
type Call struct {
	Channel   domain.Channel
	Recipient string
	Content   string
}

// MockProvider is a deterministic ports.Provider implementation for tests
// and local development. Configure it with functional options:
//
//	provider.NewMockProvider(
//	    provider.WithSuccessRate(0),
//	    provider.WithFailureMode(provider.FailurePermanent),
//	    provider.WithLatency(50*time.Millisecond),
//	)
//
// Defaults: success rate 1.0, transient failure mode (used when rate < 1),
// zero latency.
type MockProvider struct {
	successRate float64
	failureMode FailureMode
	latency     time.Duration

	mu    sync.Mutex
	calls []Call

	// counter cycles 0..(steps-1) so a 0.5 rate alternates success/failure
	// deterministically rather than rolling a die. Tests stay reproducible.
	counter atomic.Uint64
}

// MockProviderOption applies a configuration tweak to a fresh MockProvider.
type MockProviderOption func(*MockProvider)

// WithSuccessRate sets the fraction of calls that succeed. Clamped to [0, 1].
// Values outside the range are treated as the nearest bound.
func WithSuccessRate(rate float64) MockProviderOption {
	return func(p *MockProvider) {
		switch {
		case rate <= 0:
			p.successRate = 0
		case rate >= 1:
			p.successRate = 1
		default:
			p.successRate = rate
		}
	}
}

// WithFailureMode picks transient (default) or permanent for the failure
// path.
func WithFailureMode(mode FailureMode) MockProviderOption {
	return func(p *MockProvider) {
		p.failureMode = mode
	}
}

// WithLatency makes every Send call wait the given duration (or until the
// context is cancelled, whichever comes first).
func WithLatency(d time.Duration) MockProviderOption {
	return func(p *MockProvider) {
		p.latency = d
	}
}

// NewMockProvider constructs a MockProvider with the supplied options
// applied to its defaults.
func NewMockProvider(opts ...MockProviderOption) *MockProvider {
	p := &MockProvider{
		successRate: 1.0,
		failureMode: FailureTransient,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Send implements ports.Provider. The call is recorded, the configured
// latency is observed (cut short on ctx cancellation), and a DeliveryResult
// reflecting the configured success rate / failure mode is returned.
func (p *MockProvider) Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult {
	p.mu.Lock()
	p.calls = append(p.calls, Call{Channel: channel, Recipient: recipient, Content: content})
	p.mu.Unlock()

	start := time.Now()
	if p.latency > 0 {
		select {
		case <-time.After(p.latency):
		case <-ctx.Done():
			return domain.TransientFailureResult(
				fmt.Sprintf("mock provider: context cancelled: %v", ctx.Err()),
				0,
				time.Since(start),
			)
		}
	}
	elapsed := time.Since(start)

	if p.shouldSucceed() {
		return domain.DeliveredResult("mock-msg-"+string(channel), elapsed)
	}
	if p.failureMode == FailurePermanent {
		return domain.PermanentFailureResult("mock provider: permanent failure", 400, elapsed)
	}
	return domain.TransientFailureResult("mock provider: transient failure", 503, elapsed)
}

// shouldSucceed implements a deterministic round-robin against successRate
// so tests do not have to seed random state. 1.0 always succeeds, 0.0 never
// does, and in between (e.g. 0.5) every Nth call succeeds.
func (p *MockProvider) shouldSucceed() bool {
	switch {
	case p.successRate >= 1:
		return true
	case p.successRate <= 0:
		return false
	default:
		// Map rate r ∈ (0,1) onto a period of 100 ticks. Tick k succeeds
		// iff k < r*100. Counter starts at 0 and advances after each call.
		tick := p.counter.Add(1) - 1
		return tick%100 < uint64(p.successRate*100)
	}
}

// Calls returns a snapshot of every Send invocation recorded so far. Safe
// to call concurrently with Send.
func (p *MockProvider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Call, len(p.calls))
	copy(out, p.calls)
	return out
}

// Reset clears the captured calls — useful between subtests that share a
// MockProvider instance.
func (p *MockProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = nil
	p.counter.Store(0)
}
