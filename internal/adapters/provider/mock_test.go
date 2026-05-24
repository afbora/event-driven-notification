package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/provider"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestMockProvider_DefaultSucceeds: out of the box (no options) the mock
// returns a DeliveredResult with a placeholder message id.
func TestMockProvider_DefaultSucceeds(t *testing.T) {
	p := provider.NewMockProvider()

	got := p.Send(context.Background(), domain.ChannelSMS, "+905551234567", "hello")
	require.True(t, got.Success)
	require.NotEmpty(t, got.MessageID, "delivered result must carry a message id")
	require.False(t, got.Retryable)
	require.Empty(t, got.Reason)
}

// TestMockProvider_AlwaysFailsPermanent: WithSuccessRate(0) +
// WithFailureMode(FailurePermanent) yields a non-retryable result.
func TestMockProvider_AlwaysFailsPermanent(t *testing.T) {
	p := provider.NewMockProvider(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailurePermanent),
	)

	got := p.Send(context.Background(), domain.ChannelSMS, "+905551234567", "x")
	require.False(t, got.Success)
	require.False(t, got.Retryable, "permanent failure must not be retryable")
	require.NotEmpty(t, got.Reason)
}

// TestMockProvider_AlwaysFailsTransient: success=0 + FailureTransient yields
// a retryable result (5xx-class).
func TestMockProvider_AlwaysFailsTransient(t *testing.T) {
	p := provider.NewMockProvider(
		provider.WithSuccessRate(0),
		provider.WithFailureMode(provider.FailureTransient),
	)

	got := p.Send(context.Background(), domain.ChannelSMS, "+905551234567", "x")
	require.False(t, got.Success)
	require.True(t, got.Retryable, "transient failure must be retryable")
	require.NotEmpty(t, got.Reason)
}

// TestMockProvider_RecordsCalls: every Send call is captured so tests can
// assert what the worker actually sent.
func TestMockProvider_RecordsCalls(t *testing.T) {
	p := provider.NewMockProvider()
	ctx := context.Background()

	p.Send(ctx, domain.ChannelSMS, "+905550000001", "first")
	p.Send(ctx, domain.ChannelEmail, "user@example.com", "second")
	p.Send(ctx, domain.ChannelPush, "fcm-token", "third")

	calls := p.Calls()
	require.Len(t, calls, 3)
	require.Equal(t, domain.ChannelSMS, calls[0].Channel)
	require.Equal(t, "+905550000001", calls[0].Recipient)
	require.Equal(t, "first", calls[0].Content)
	require.Equal(t, domain.ChannelEmail, calls[1].Channel)
	require.Equal(t, domain.ChannelPush, calls[2].Channel)
}

// TestMockProvider_RespectsLatency: configured latency shows up on the
// DeliveryResult.Latency field — the worker reports this on the
// processing-duration histogram (phase 6).
func TestMockProvider_RespectsLatency(t *testing.T) {
	p := provider.NewMockProvider(provider.WithLatency(50 * time.Millisecond))

	start := time.Now()
	got := p.Send(context.Background(), domain.ChannelSMS, "+9", "x")
	elapsed := time.Since(start)

	require.True(t, got.Success)
	require.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "send must wait the configured latency")
	require.GreaterOrEqual(t, got.Latency, 50*time.Millisecond, "DeliveryResult.Latency must reflect the wait")
}

// TestMockProvider_ContextCancellation: a cancelled context cuts the
// configured latency short and yields a transient failure (timeout-ish).
func TestMockProvider_ContextCancellation(t *testing.T) {
	p := provider.NewMockProvider(provider.WithLatency(5 * time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := p.Send(ctx, domain.ChannelSMS, "+9", "x")
	elapsed := time.Since(start)

	require.False(t, got.Success)
	require.True(t, got.Retryable, "context cancellation surfaces as transient failure")
	require.Less(t, elapsed, 1*time.Second, "must not wait the full latency when ctx is cancelled")
}
