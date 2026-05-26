package application

import "errors"

// Application-level sentinel errors. ProcessNotification returns these
// from its terminal helpers so the asynq processor — and through it
// the worker's RetryDelayFunc — can route the retry timing per cause
// without parsing strings (CLAUDE.md §3.5 forbids string compares on
// errors; use errors.Is / errors.As).
//
// These are NOT domain errors — they encode infrastructure-level retry
// signals the use case sends back to the asynq layer. Domain errors
// live in internal/domain/errors.go.
var (
	// ErrProviderTransient signals the upstream provider failed in a
	// retryable way (5xx-class, timeout, network blip). Asynq's
	// RetryDelayFunc maps this to the exponential backoff schedule.
	ErrProviderTransient = errors.New("provider transient failure")

	// ErrOutboundRateLimited signals the worker's per-channel rate
	// limiter rejected the send. Asynq's RetryDelayFunc maps this to
	// the short rate-limit backoff so the throttled task re-fires
	// quickly once the window rolls forward — distinct from the
	// exponential schedule transient failures use.
	ErrOutboundRateLimited = errors.New("outbound rate limit exceeded")
)
