package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/adapters/provider"
	"github.com/afbora/event-driven-notification/internal/domain"
)

// TestRegistry_RegisterAndSend: a provider registered for SMS receives the
// Send call when the registry is invoked with ChannelSMS.
func TestRegistry_RegisterAndSend(t *testing.T) {
	sms := provider.NewMockProvider()
	email := provider.NewMockProvider()

	r := provider.NewRegistry()
	r.Register(domain.ChannelSMS, sms)
	r.Register(domain.ChannelEmail, email)

	got := r.Send(context.Background(), domain.ChannelSMS, "+905551234567", "hello")
	require.True(t, got.Success)

	require.Len(t, sms.Calls(), 1, "sms provider must have received the call")
	require.Empty(t, email.Calls(), "email provider must be untouched")
}

// TestRegistry_UnknownChannel: a channel with no registered provider yields
// a permanent failure — the worker should mark the notification failed
// rather than retrying forever.
func TestRegistry_UnknownChannel(t *testing.T) {
	r := provider.NewRegistry()
	// Nothing registered.

	got := r.Send(context.Background(), domain.ChannelPush, "fcm-token", "x")
	require.False(t, got.Success)
	require.False(t, got.Retryable, "missing provider is a permanent failure (config bug)")
	require.NotEmpty(t, got.Reason)
}

// TestRegistry_ReplaceProvider: re-registering a channel replaces the
// previous provider — useful for tests that want to swap mocks.
func TestRegistry_ReplaceProvider(t *testing.T) {
	first := provider.NewMockProvider()
	second := provider.NewMockProvider()

	r := provider.NewRegistry()
	r.Register(domain.ChannelSMS, first)
	r.Register(domain.ChannelSMS, second)

	r.Send(context.Background(), domain.ChannelSMS, "+9", "x")

	require.Empty(t, first.Calls(), "first provider must have been replaced")
	require.Len(t, second.Calls(), 1)
}

// TestRegistry_RoutingByChannel: a single Send call routes to exactly one
// provider — the one registered for that channel.
func TestRegistry_RoutingByChannel(t *testing.T) {
	sms := provider.NewMockProvider()
	email := provider.NewMockProvider()
	push := provider.NewMockProvider()

	r := provider.NewRegistry()
	r.Register(domain.ChannelSMS, sms)
	r.Register(domain.ChannelEmail, email)
	r.Register(domain.ChannelPush, push)

	ctx := context.Background()
	r.Send(ctx, domain.ChannelSMS, "+9", "a")
	r.Send(ctx, domain.ChannelEmail, "a@b.c", "b")
	r.Send(ctx, domain.ChannelPush, "tok", "c")
	r.Send(ctx, domain.ChannelEmail, "x@y.z", "d")

	require.Len(t, sms.Calls(), 1)
	require.Len(t, email.Calls(), 2)
	require.Len(t, push.Calls(), 1)
}
