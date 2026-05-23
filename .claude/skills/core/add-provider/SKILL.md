# Skill: add-provider

## Purpose

Add a new notification provider implementation (e.g., a new webhook target, a real Twilio integration, a new mock variant for testing) without modifying business logic.

## When To Use

- A new channel is being added (e.g., WhatsApp, in-app).
- A real provider integration is replacing a webhook (e.g., switching SMS from generic webhook to Twilio).
- A new test-only provider variant is needed (e.g., one that simulates intermittent failures).

## Prerequisites

- The `Provider` port is defined in `internal/ports/provider.go`.
- You know the channel(s) this provider will serve.
- For real providers: credentials/endpoint URLs are config-only (no secrets in code).

## Steps

### 1. Confirm the `Provider` port covers what you need

The port interface (in `internal/ports/provider.go`) should look like:

```go
type Provider interface {
    Send(ctx context.Context, n domain.Notification) (*DeliveryResult, error)
    Channels() []domain.Channel
}
```

If your new provider needs something the port does not expose (e.g., bulk send, async receipts), **stop and discuss with the human**. Changing the port affects every other implementation.

### 2. Create the implementation file

In `internal/adapters/provider/<name>.go`:

```go
package provider

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/<repo>/api/internal/domain"
    "github.com/<repo>/api/internal/ports"
)

// TwilioProvider sends SMS via the Twilio REST API.
// Configuration is supplied at construction; the struct is safe for
// concurrent use across many goroutines.
type TwilioProvider struct {
    httpClient *http.Client
    accountSID string
    authToken  string
    fromNumber string
}

func NewTwilioProvider(httpClient *http.Client, accountSID, authToken, fromNumber string) *TwilioProvider {
    if httpClient == nil {
        httpClient = &http.Client{Timeout: 10 * time.Second}
    }
    return &TwilioProvider{
        httpClient: httpClient,
        accountSID: accountSID,
        authToken:  authToken,
        fromNumber: fromNumber,
    }
}

func (p *TwilioProvider) Channels() []domain.Channel {
    return []domain.Channel{domain.ChannelSMS}
}

func (p *TwilioProvider) Send(ctx context.Context, n domain.Notification) (*ports.DeliveryResult, error) {
    // 1. Build request
    // 2. Apply context (timeout, cancellation)
    // 3. Execute
    // 4. Classify response: 2xx success, 4xx permanent, 5xx/timeout transient
    // 5. Return DeliveryResult with provider message ID, or wrapped error
}
```

Commit pair (test first):

- `test(adapter/provider): add failing test for <provider> provider`
- `feat(adapter/provider): implement <provider> provider`

### 3. Write the test

Use `httptest.NewServer` to simulate the provider's API. Do NOT call the real provider in tests. Cover:

- Happy path (200/202 with provider message ID).
- Permanent failure (4xx) → returns `ports.ErrPermanent`.
- Transient failure (5xx) → returns wrapped error compatible with retry.
- Timeout → context-deadline exceeded mapped to transient.
- Malformed response → returns error.

### 4. Register the provider with the `ProviderRegistry`

In the registry setup (typically in `cmd/api/main.go` and `cmd/worker/main.go`), register by channel:

```go
registry := provider.NewRegistry()
registry.Register(domain.ChannelSMS, twilioProvider)
registry.Register(domain.ChannelEmail, sendgridProvider)
registry.Register(domain.ChannelPush, fcmProvider)
```

If the new provider replaces an existing one, update the registration site.

### 5. Wrap the provider in the circuit breaker decorator

All providers go through the circuit breaker. The wiring should already exist in `cmd/worker/main.go`:

```go
sender := circuit.NewBreaker(twilioProvider, circuit.Settings{
    Threshold: 5,
    Timeout:   30 * time.Second,
})
registry.Register(domain.ChannelSMS, sender)
```

If you are introducing a new channel, ensure circuit breaker configuration is also added for it.

### 6. Add provider-specific metrics labels

In `internal/infrastructure/metrics`, the `provider` label is already used. Confirm your provider's `Channels()` returns the right channels so the labels stay consistent.

If the provider exposes a unique error code worth tracking, add a `reason` label value documented in `metrics.go`.

### 7. Add configuration

In `internal/infrastructure/config`, add the new provider's config fields:

```go
type ProviderConfig struct {
    Driver        string        // "webhook", "twilio", "sendgrid", "mock"
    Webhook       WebhookConfig `mapstructure:"webhook"`
    Twilio        TwilioConfig  `mapstructure:"twilio"`
    // ...
}
```

Update `.env.example` with the new variables.

### 8. Document the provider

If this is a real integration, add a short note to README under the "Providers" section:

> **Twilio** — SMS via the Twilio REST API. Requires `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN`, `TWILIO_FROM_NUMBER`. Maps Twilio error codes 21xxx to permanent failure (no retry), 5xx and network errors to transient (retry).

### 9. Add an integration test if the provider is real

In `tests/integration/provider/`, add a test that runs against a sandbox account or a mock server that mirrors the provider's API closely. Tag with `//go:build integration`.

## Verification

- [ ] `make test` passes.
- [ ] `make lint` passes.
- [ ] Provider is registered in `cmd/api` and `cmd/worker` if applicable.
- [ ] Circuit breaker wraps it.
- [ ] Metrics labels are correct.
- [ ] Configuration is documented in `.env.example`.
- [ ] README is updated if the provider is user-facing.

## Common Mistakes

- Skipping error classification. A 4xx is permanent (no retry); a 5xx or timeout is transient (retry). Conflating them either spams retries or drops messages.
- Returning the raw HTTP body in error messages — leaks credentials or PII into logs.
- Forgetting to apply context to the HTTP request (`req.WithContext(ctx)`). Cancellation will not work.
- Using `http.DefaultClient` (no timeout). Always pass a configured client.
- Hardcoding endpoints. Always config.
- Putting provider logic in business code. The provider is an adapter; the worker calls `registry.Get(channel).Send(ctx, notification)` and never knows which provider it got.
- Forgetting to circuit-break a new provider, defeating the purpose of the existing decorator chain.
