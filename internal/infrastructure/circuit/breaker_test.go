package circuit_test

import (
	"context"
	"testing"
	"time"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/provider"
	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/infrastructure/circuit"
)

// tightSettings returns gobreaker Settings tuned for fast tests — 3
// consecutive failures trips, 50ms timeout before half-open probe.
func tightSettings(name string) gobreaker.Settings {
	return gobreaker.Settings{
		Name:        name,
		MaxRequests: 1,
		Interval:    500 * time.Millisecond,
		Timeout:     50 * time.Millisecond,
		ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= 3 },
	}
}

// TestBreaker_SuccessPassesThrough: a healthy provider's result reaches
// the caller unchanged; the breaker stays closed.
func TestBreaker_SuccessPassesThrough(t *testing.T) {
	inner := provider.NewMockProvider() // default: always succeeds
	b := circuit.New(inner, tightSettings("test-success"))

	got := b.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	require.True(t, got.Success)
	require.Len(t, inner.Calls(), 1)
}

// TestBreaker_TransientFailureTripsBreaker: after enough consecutive
// transient failures the breaker opens; subsequent calls fail fast
// without touching the inner provider.
func TestBreaker_TransientFailureTripsBreaker(t *testing.T) {
	inner := provider.NewMockProvider(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)
	b := circuit.New(inner, tightSettings("test-trip"))

	ctx := context.Background()

	// Burn through the threshold (3 in tightSettings).
	for i := 0; i < 3; i++ {
		got := b.Send(ctx, domain.ChannelSMS, "+9", "x")
		require.False(t, got.Success, "call %d should fail", i+1)
	}

	innerCallsAfterTrip := len(inner.Calls())

	// Breaker is open now — next call must fail fast, never reach inner.
	got := b.Send(ctx, domain.ChannelSMS, "+9", "x")
	require.False(t, got.Success)
	require.True(t, got.Retryable, "circuit-open failure is transient")
	require.Len(t, inner.Calls(), innerCallsAfterTrip,
		"inner provider must not be called while breaker is open")
}

// TestBreaker_PermanentFailureDoesNotTrip: 4xx-class results are caller
// errors, not provider sickness. Breaker stays closed even after many
// consecutive permanent failures.
func TestBreaker_PermanentFailureDoesNotTrip(t *testing.T) {
	inner := provider.NewMockProvider(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailurePermanent),
	)
	b := circuit.New(inner, tightSettings("test-permanent"))

	ctx := context.Background()

	// Way more than the threshold.
	for i := 0; i < 10; i++ {
		got := b.Send(ctx, domain.ChannelSMS, "+9", "x")
		require.False(t, got.Success)
		require.False(t, got.Retryable, "permanent failures stay permanent")
	}

	// Every call reached the inner provider — breaker never tripped.
	require.Len(t, inner.Calls(), 10)
}

// TestBreaker_HalfOpenRecovery: once the open-state timeout elapses, the
// breaker enters half-open. A successful probe closes it again; the next
// call reaches the inner provider normally.
func TestBreaker_HalfOpenRecovery(t *testing.T) {
	// Start the inner as a controllable mock — first three calls fail
	// transiently, subsequent calls succeed.
	inner := provider.NewMockProvider(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)
	b := circuit.New(inner, tightSettings("test-recovery"))

	ctx := context.Background()

	// Trip the breaker.
	for i := 0; i < 3; i++ {
		_ = b.Send(ctx, domain.ChannelSMS, "+9", "x")
	}

	// Immediately after trip — breaker open, call fails fast (inner untouched).
	callsBeforeWait := len(inner.Calls())
	got := b.Send(ctx, domain.ChannelSMS, "+9", "x")
	require.False(t, got.Success)
	require.Len(t, inner.Calls(), callsBeforeWait, "breaker open: inner not called")

	// Sleep past the open-timeout. The next call should enter the half-open
	// state and let one probe through to the inner provider.
	time.Sleep(75 * time.Millisecond)

	gotAfter := b.Send(ctx, domain.ChannelSMS, "+9", "x")
	require.False(t, gotAfter.Success, "inner still fails on the probe")
	require.Greater(t, len(inner.Calls()), callsBeforeWait,
		"after timeout, breaker should let a probe through to the inner provider")
}
