package provider

import (
	"context"
	"fmt"
	"sync"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// Registry routes Send calls to a per-channel provider implementation.
// It itself satisfies ports.Provider, so the worker can depend on a single
// Provider port and the registry handles strategy dispatch internally
// (ADR-0004). Adding a new channel means writing one new Provider
// implementation and one Register call in cmd/api/main.go — no switch
// statements in business logic.
//
// Replacement semantics: Register on a channel that already has a provider
// replaces it. Useful for tests; in production each channel is wired exactly
// once at startup.
type Registry struct {
	mu        sync.RWMutex
	providers map[domain.Channel]ports.Provider
}

// NewRegistry returns an empty registry. Callers Register before the
// registry is exposed to the worker.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[domain.Channel]ports.Provider)}
}

// Register associates a provider with a channel, replacing any previous
// registration for that channel.
func (r *Registry) Register(channel domain.Channel, p ports.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[channel] = p
}

// Send dispatches to the provider registered for the given channel. A
// missing provider yields a permanent failure — this is a configuration
// bug (no channel routing wired up) and retrying will not help.
func (r *Registry) Send(ctx context.Context, channel domain.Channel, recipient, content string) domain.DeliveryResult {
	r.mu.RLock()
	p, ok := r.providers[channel]
	r.mu.RUnlock()
	if !ok {
		return domain.PermanentFailureResult(
			fmt.Sprintf("provider registry: no provider registered for channel %q", channel),
			0,
			0,
		)
	}
	return p.Send(ctx, channel, recipient, content)
}
